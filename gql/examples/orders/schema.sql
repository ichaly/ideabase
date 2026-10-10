-- Apply once with the application's migration owner, before starting replicas.
CREATE TABLE customers (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id text NOT NULL,
  name text NOT NULL,
  UNIQUE (tenant_id, id)
);
CREATE TABLE products (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id text NOT NULL,
  name text NOT NULL,
  price_cents integer NOT NULL CHECK (price_cents >= 0),
  stock integer NOT NULL CHECK (stock >= 0),
  UNIQUE (tenant_id, id)
);
CREATE TABLE orders (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id text NOT NULL,
  customer_id bigint NOT NULL,
  request_id text NOT NULL CHECK (length(request_id) BETWEEN 1 AND 128),
  request_body jsonb NOT NULL,
  status text NOT NULL DEFAULT 'placed' CHECK (status IN ('placed', 'cancelled')),
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by text NOT NULL,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, request_id),
  FOREIGN KEY (tenant_id, customer_id) REFERENCES customers(tenant_id, id)
);
CREATE TABLE order_items (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id text NOT NULL,
  order_id uuid NOT NULL,
  product_id bigint NOT NULL,
  quantity integer NOT NULL CHECK (quantity BETWEEN 1 AND 10000),
  price_cents integer NOT NULL CHECK (price_cents >= 0),
  UNIQUE (tenant_id, order_id, product_id),
  FOREIGN KEY (tenant_id, order_id) REFERENCES orders(tenant_id, id),
  FOREIGN KEY (tenant_id, product_id) REFERENCES products(tenant_id, id)
);
CREATE INDEX orders_tenant_recent ON orders(tenant_id, created_at DESC, id);
CREATE INDEX orders_customer ON orders(tenant_id, customer_id);
CREATE INDEX order_items_product ON order_items(tenant_id, product_id);
-- Policies also protect SQL written in business resolvers.
DO $$ DECLARE t text; BEGIN
  FOREACH t IN ARRAY ARRAY['customers','products','orders','order_items'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant ON %I USING (tenant_id = current_setting(''gql.tenant'', true)) WITH CHECK (tenant_id = current_setting(''gql.tenant'', true))', t);
  END LOOP;
END $$;
