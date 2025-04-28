package metadata

import (
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestDialectMySQL_GetMetadataQuery(t *testing.T) {
	// 创建模拟的数据库连接
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("创建mock失败: %v", err)
	}
	defer db.Close()

	// 设置版本检查的预期
	mock.ExpectQuery("SELECT VERSION()").WillReturnRows(
		sqlmock.NewRows([]string{"VERSION()"}).AddRow("8.0.28"),
	)

	// 创建GORM数据库连接
	gormDB, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      db,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{})
	if err != nil {
		t.Fatalf("创建GORM连接失败: %v", err)
	}

	// 创建MySQL方言实例
	dialect, err := NewDialectMySQL(gormDB, "test_db")
	assert.NoError(t, err)

	// 模拟元数据查询结果
	mock.ExpectQuery("WITH tables AS").WillReturnRows(
		sqlmock.NewRows([]string{"metadata"}).AddRow(
			`{
				"tables": [
					{"table_name": "users", "table_description": "用户表"},
					{"table_name": "posts", "table_description": "文章表"}
				],
				"columns": [
					{
						"table_name": "users",
						"column_name": "id",
						"data_type": "int",
						"is_nullable": false,
						"character_maximum_length": null,
						"numeric_precision": 10,
						"numeric_scale": 0,
						"column_description": "用户ID"
					}
				],
				"primaryKeys": [
					{"table_name": "users", "column_name": "id"}
				],
				"foreignKeys": [
					{
						"source_table": "posts",
						"source_column": "user_id",
						"target_table": "users",
						"target_column": "id"
					}
				]
			}`,
		),
	)

	// 执行查询
	query, args := dialect.GetMetadataQuery()
	rows, err := gormDB.Raw(query, args...).Rows()
	assert.NoError(t, err)
	defer rows.Close()

	// 读取结果
	var result struct {
		Tables      []map[string]string      `json:"tables"`
		Columns     []map[string]interface{} `json:"columns"`
		PrimaryKeys []map[string]string      `json:"primaryKeys"`
		ForeignKeys []map[string]interface{} `json:"foreignKeys"`
	}

	if rows.Next() {
		var metadata string
		err = rows.Scan(&metadata)
		assert.NoError(t, err)
		err = json.Unmarshal([]byte(metadata), &result)
		assert.NoError(t, err)
	}

	// 验证结果
	assert.Len(t, result.Tables, 2)
	assert.Equal(t, "users", result.Tables[0]["table_name"])
	assert.Equal(t, "posts", result.Tables[1]["table_name"])

	// 验证没有系统表
	for _, table := range result.Tables {
		assert.NotContains(t, table["table_name"], "information_schema")
		assert.NotContains(t, table["table_name"], "mysql")
		assert.NotContains(t, table["table_name"], "performance_schema")
		assert.NotContains(t, table["table_name"], "sys")
		assert.NotContains(t, table["table_name"], "innodb")
	}

	// 验证外键关系
	assert.Len(t, result.ForeignKeys, 1)
	fk := result.ForeignKeys[0]
	assert.Equal(t, "posts", fk["source_table"])
	assert.Equal(t, "users", fk["target_table"])

	// 验证mock期望被满足
	assert.NoError(t, mock.ExpectationsWereMet())
}
