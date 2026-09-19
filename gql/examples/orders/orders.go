package main

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/ichaly/ideabase/gql"
)

type OrderLine struct {
	ProductID int64 `json:"productId"`
	Quantity  int   `json:"quantity"`
}
type placeArgs struct {
	RequestID  string      `json:"requestId"`
	CustomerID int64       `json:"customerId"`
	Items      []OrderLine `json:"items"`
}
type OrderReceipt struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// The engine owns the transaction; retries use the same tenant-scoped requestId.
func placeOrder(ctx context.Context, _ gql.Root, args placeArgs) (OrderReceipt, error) {
	var receipt OrderReceipt
	if strings.TrimSpace(args.RequestID) == "" || len(args.RequestID) > 128 || args.CustomerID <= 0 || len(args.Items) == 0 || len(args.Items) > 100 {
		return receipt, errors.New("invalid order")
	}
	// Stable lock order avoids deadlocks between overlapping shopping baskets.
	args.Items = slices.Clone(args.Items)
	slices.SortFunc(args.Items, func(a, b OrderLine) int { return cmp.Compare(a.ProductID, b.ProductID) })
	ids, quantities := make([]int64, len(args.Items)), make([]int32, len(args.Items))
	for i, item := range args.Items {
		if item.ProductID <= 0 || item.Quantity <= 0 || item.Quantity > 10000 || (i > 0 && item.ProductID == args.Items[i-1].ProductID) {
			return receipt, errors.New("invalid order line")
		}
		ids[i], quantities[i] = item.ProductID, int32(item.Quantity)
	}
	execution, ok := gql.ExecutionFrom(ctx)
	if !ok || execution.Transaction() == nil {
		return receipt, errors.New("order requires engine transaction")
	}
	tenant, actor := execution.Identity().Tenant(), execution.Identity().Actor()
	payload, err := json.Marshal(struct {
		CustomerID int64
		Items      []OrderLine
	}{args.CustomerID, args.Items})
	if err != nil {
		return receipt, err
	}
	db := execution.Transaction()
	err = db.QueryRowContext(ctx, `INSERT INTO orders(tenant_id,customer_id,request_id,request_body,created_by,updated_by)
 VALUES ($1,$2,$3,$4::jsonb,$5,$5) ON CONFLICT (tenant_id,request_id) DO NOTHING RETURNING id,status`, tenant, args.CustomerID, args.RequestID, string(payload), actor).Scan(&receipt.ID, &receipt.Status)
	if errors.Is(err, sql.ErrNoRows) {
		var same bool
		err = db.QueryRowContext(ctx, `SELECT id,status,request_body=$3::jsonb FROM orders WHERE tenant_id=$1 AND request_id=$2`, tenant, args.RequestID, string(payload)).Scan(&receipt.ID, &receipt.Status, &same)
		if err == nil && !same {
			err = errors.New("requestId already used for another order")
		}
		return receipt, err
	}
	if err != nil {
		return receipt, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id,stock FROM products WHERE tenant_id=$1 AND id=ANY($2::bigint[]) ORDER BY id FOR UPDATE`, tenant, ids)
	if err != nil {
		return receipt, err
	}
	count := 0
	for rows.Next() {
		var id int64
		var stock int
		if err = rows.Scan(&id, &stock); err != nil {
			break
		}
		if count >= len(ids) || id != ids[count] || stock < int(quantities[count]) {
			err = errors.New("product unavailable or insufficient stock")
			break
		}
		count++
	}
	scanErr := rows.Err()
	rows.Close()
	if err != nil {
		return receipt, err
	}
	if scanErr != nil {
		return receipt, scanErr
	}
	if count != len(ids) {
		return receipt, errors.New("product unavailable")
	}
	_, err = db.ExecContext(ctx, `UPDATE products p SET stock=p.stock-x.quantity
 FROM unnest($2::bigint[],$3::integer[]) AS x(id,quantity) WHERE p.tenant_id=$1 AND p.id=x.id`, tenant, ids, quantities)
	if err != nil {
		return receipt, err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO order_items(tenant_id,order_id,product_id,quantity,price_cents)
 SELECT $1,$2,p.id,x.quantity,p.price_cents FROM unnest($3::bigint[],$4::integer[]) AS x(id,quantity)
 JOIN products p ON p.id=x.id AND p.tenant_id=$1`, tenant, receipt.ID, ids, quantities)
	if err != nil {
		return receipt, err
	}
	return receipt, execution.Invalidate("orders", "order_items", "products")
}

type cancelArgs struct {
	ID string `json:"id"`
}

func cancelOrder(ctx context.Context, _ gql.Root, args cancelArgs) (OrderReceipt, error) {
	var receipt OrderReceipt
	execution, ok := gql.ExecutionFrom(ctx)
	if !ok || execution.Transaction() == nil {
		return receipt, errors.New("order requires engine transaction")
	}
	db, tenant := execution.Transaction(), execution.Identity().Tenant()
	err := db.QueryRowContext(ctx, `SELECT id,status FROM orders WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, args.ID).Scan(&receipt.ID, &receipt.Status)
	if err != nil || receipt.Status == "cancelled" {
		return receipt, err
	}
	// Use the same ascending product lock order as placeOrder.
	rows, err := db.QueryContext(ctx, `SELECT p.id FROM products p JOIN order_items i ON i.tenant_id=p.tenant_id AND i.product_id=p.id
 WHERE i.tenant_id=$1 AND i.order_id=$2 ORDER BY p.id FOR UPDATE OF p`, tenant, args.ID)
	if err != nil {
		return receipt, err
	}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
	}
	scanErr := rows.Err()
	rows.Close()
	if err != nil {
		return receipt, err
	}
	if scanErr != nil {
		return receipt, scanErr
	}
	_, err = db.ExecContext(ctx, `UPDATE products p SET stock=p.stock+i.quantity FROM order_items i
 WHERE i.tenant_id=$1 AND i.order_id=$2 AND p.tenant_id=i.tenant_id AND p.id=i.product_id`, tenant, args.ID)
	if err != nil {
		return receipt, err
	}
	_, err = db.ExecContext(ctx, `UPDATE orders SET status='cancelled',updated_at=now(),updated_by=$3 WHERE tenant_id=$1 AND id=$2`, tenant, args.ID, execution.Identity().Actor())
	if err != nil {
		return receipt, err
	}
	receipt.Status = "cancelled"
	return receipt, execution.Invalidate("orders", "products")
}
