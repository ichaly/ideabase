package pgsql

import (
	"testing"

	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/stretchr/testify/assert"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestWhereBuilder(t *testing.T) {
	dialect := &Dialect{}

	tests := []struct {
		name     string
		args     ast.ArgumentList
		expected string
		wantErr  bool
	}{
		{
			name: "简单等值条件",
			args: ast.ArgumentList{
				{
					Name: gql.WHERE,
					Value: &ast.Value{
						Kind: ast.ObjectValue,
						Children: []*ast.ChildValue{
							{
								Name: "id",
								Value: &ast.Value{
									Kind: ast.ObjectValue,
									Children: []*ast.ChildValue{
										{
											Name: gql.EQ,
											Value: &ast.Value{
												Kind: ast.IntValue,
												Raw:  "1",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expected: ` WHERE "id" = $1`,
			wantErr:  false,
		},
		{
			name: "IN条件",
			args: ast.ArgumentList{
				{
					Name: gql.WHERE,
					Value: &ast.Value{
						Kind: ast.ObjectValue,
						Children: []*ast.ChildValue{
							{
								Name: "status",
								Value: &ast.Value{
									Kind: ast.ObjectValue,
									Children: []*ast.ChildValue{
										{
											Name: gql.IN,
											Value: &ast.Value{
												Kind: ast.ListValue,
												Children: []*ast.ChildValue{
													{
														Value: &ast.Value{
															Kind: ast.StringValue,
															Raw:  "active",
														},
													},
													{
														Value: &ast.Value{
															Kind: ast.StringValue,
															Raw:  "pending",
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expected: ` WHERE "status" IN ($1, $2)`,
			wantErr:  false,
		},
		{
			name: "AND条件",
			args: ast.ArgumentList{
				{
					Name: gql.WHERE,
					Value: &ast.Value{
						Kind: ast.ObjectValue,
						Children: []*ast.ChildValue{
							{
								Name: gql.AND,
								Value: &ast.Value{
									Kind: ast.ListValue,
									Children: []*ast.ChildValue{
										{
											Value: &ast.Value{
												Kind: ast.ObjectValue,
												Children: []*ast.ChildValue{
													{
														Name: "age",
														Value: &ast.Value{
															Kind: ast.ObjectValue,
															Children: []*ast.ChildValue{
																{
																	Name: gql.GT,
																	Value: &ast.Value{
																		Kind: ast.IntValue,
																		Raw:  "18",
																	},
																},
															},
														},
													},
												},
											},
										},
										{
											Value: &ast.Value{
												Kind: ast.ObjectValue,
												Children: []*ast.ChildValue{
													{
														Name: "status",
														Value: &ast.Value{
															Kind: ast.ObjectValue,
															Children: []*ast.ChildValue{
																{
																	Name: gql.EQ,
																	Value: &ast.Value{
																		Kind: ast.StringValue,
																		Raw:  "active",
																	},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expected: ` WHERE ("age" > $1 AND "status" = $2)`,
			wantErr:  false,
		},
		{
			name: "OR条件",
			args: ast.ArgumentList{
				{
					Name: gql.WHERE,
					Value: &ast.Value{
						Kind: ast.ObjectValue,
						Children: []*ast.ChildValue{
							{
								Name: gql.OR,
								Value: &ast.Value{
									Kind: ast.ListValue,
									Children: []*ast.ChildValue{
										{
											Value: &ast.Value{
												Kind: ast.ObjectValue,
												Children: []*ast.ChildValue{
													{
														Name: "type",
														Value: &ast.Value{
															Kind: ast.ObjectValue,
															Children: []*ast.ChildValue{
																{
																	Name: gql.EQ,
																	Value: &ast.Value{
																		Kind: ast.StringValue,
																		Raw:  "admin",
																	},
																},
															},
														},
													},
												},
											},
										},
										{
											Value: &ast.Value{
												Kind: ast.ObjectValue,
												Children: []*ast.ChildValue{
													{
														Name: "role",
														Value: &ast.Value{
															Kind: ast.ObjectValue,
															Children: []*ast.ChildValue{
																{
																	Name: gql.EQ,
																	Value: &ast.Value{
																		Kind: ast.StringValue,
																		Raw:  "manager",
																	},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expected: ` WHERE ("type" = $1 OR "role" = $2)`,
			wantErr:  false,
		},
		{
			name: "NOT条件",
			args: ast.ArgumentList{
				{
					Name: gql.WHERE,
					Value: &ast.Value{
						Kind: ast.ObjectValue,
						Children: []*ast.ChildValue{
							{
								Name: gql.NOT,
								Value: &ast.Value{
									Kind: ast.ObjectValue,
									Children: []*ast.ChildValue{
										{
											Name: "deleted",
											Value: &ast.Value{
												Kind: ast.ObjectValue,
												Children: []*ast.ChildValue{
													{
														Name: gql.EQ,
														Value: &ast.Value{
															Kind: ast.BooleanValue,
															Raw:  "true",
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expected: ` WHERE NOT ("deleted" = $1)`,
			wantErr:  false,
		},
		{
			name: "IS NULL条件",
			args: ast.ArgumentList{
				{
					Name: gql.WHERE,
					Value: &ast.Value{
						Kind: ast.ObjectValue,
						Children: []*ast.ChildValue{
							{
								Name: "deleted_at",
								Value: &ast.Value{
									Kind: ast.ObjectValue,
									Children: []*ast.ChildValue{
										{
											Name: gql.IS,
											Value: &ast.Value{
												Kind: ast.BooleanValue,
												Raw:  "true",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expected: ` WHERE "deleted_at" IS NULL`,
			wantErr:  false,
		},
		{
			name: "LIKE条件",
			args: ast.ArgumentList{
				{
					Name: gql.WHERE,
					Value: &ast.Value{
						Kind: ast.ObjectValue,
						Children: []*ast.ChildValue{
							{
								Name: "name",
								Value: &ast.Value{
									Kind: ast.ObjectValue,
									Children: []*ast.ChildValue{
										{
											Name: gql.LIKE,
											Value: &ast.Value{
												Kind: ast.StringValue,
												Raw:  "%admin%",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expected: ` WHERE "name" LIKE $1`,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := compiler.NewContext(nil, dialect.Quotation(), nil)

			err := dialect.buildWhere(ctx, tt.args)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			result := formatSQL(ctx.String())
			expected := formatSQL(tt.expected)
			assert.Equal(t, expected, result)
		})
	}
}

func TestWhereBuilderAdvanced(t *testing.T) {
	dialect := &Dialect{}

	tests := []struct {
		name     string
		args     ast.ArgumentList
		expected string
		wantErr  bool
	}{
		{
			name: "复杂嵌套条件",
			args: ast.ArgumentList{
				{
					Name: gql.WHERE,
					Value: &ast.Value{
						Kind: ast.ObjectValue,
						Children: []*ast.ChildValue{
							{
								Name: gql.AND,
								Value: &ast.Value{
									Kind: ast.ListValue,
									Children: []*ast.ChildValue{
										{
											Value: &ast.Value{
												Kind: ast.ObjectValue,
												Children: []*ast.ChildValue{
													{
														Name: "status",
														Value: &ast.Value{
															Kind: ast.ObjectValue,
															Children: []*ast.ChildValue{
																{
																	Name: gql.EQ,
																	Value: &ast.Value{
																		Kind: ast.StringValue,
																		Raw:  "active",
																	},
																},
															},
														},
													},
												},
											},
										},
										{
											Value: &ast.Value{
												Kind: ast.ObjectValue,
												Children: []*ast.ChildValue{
													{
														Name: gql.OR,
														Value: &ast.Value{
															Kind: ast.ListValue,
															Children: []*ast.ChildValue{
																{
																	Value: &ast.Value{
																		Kind: ast.ObjectValue,
																		Children: []*ast.ChildValue{
																			{
																				Name: "age",
																				Value: &ast.Value{
																					Kind: ast.ObjectValue,
																					Children: []*ast.ChildValue{
																						{
																							Name: gql.GT,
																							Value: &ast.Value{
																								Kind: ast.IntValue,
																								Raw:  "18",
																							},
																						},
																					},
																				},
																			},
																		},
																	},
																},
																{
																	Value: &ast.Value{
																		Kind: ast.ObjectValue,
																		Children: []*ast.ChildValue{
																			{
																				Name: "role",
																				Value: &ast.Value{
																					Kind: ast.ObjectValue,
																					Children: []*ast.ChildValue{
																						{
																							Name: gql.EQ,
																							Value: &ast.Value{
																								Kind: ast.StringValue,
																								Raw:  "admin",
																							},
																						},
																					},
																				},
																			},
																		},
																	},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expected: ` WHERE ("status" = $1 AND ("age" > $2 OR "role" = $3))`,
			wantErr:  false,
		},
		{
			name: "多个比较操作符",
			args: ast.ArgumentList{
				{
					Name: gql.WHERE,
					Value: &ast.Value{
						Kind: ast.ObjectValue,
						Children: []*ast.ChildValue{
							{
								Name: gql.AND,
								Value: &ast.Value{
									Kind: ast.ListValue,
									Children: []*ast.ChildValue{
										{
											Value: &ast.Value{
												Kind: ast.ObjectValue,
												Children: []*ast.ChildValue{
													{
														Name: "price",
														Value: &ast.Value{
															Kind: ast.ObjectValue,
															Children: []*ast.ChildValue{
																{
																	Name: gql.GE,
																	Value: &ast.Value{
																		Kind: ast.FloatValue,
																		Raw:  "10.0",
																	},
																},
															},
														},
													},
												},
											},
										},
										{
											Value: &ast.Value{
												Kind: ast.ObjectValue,
												Children: []*ast.ChildValue{
													{
														Name: "price",
														Value: &ast.Value{
															Kind: ast.ObjectValue,
															Children: []*ast.ChildValue{
																{
																	Name: gql.LE,
																	Value: &ast.Value{
																		Kind: ast.FloatValue,
																		Raw:  "100.0",
																	},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expected: ` WHERE ("price" >= $1 AND "price" <= $2)`,
			wantErr:  false,
		},
		{
			name: "ILIKE和正则表达式",
			args: ast.ArgumentList{
				{
					Name: gql.WHERE,
					Value: &ast.Value{
						Kind: ast.ObjectValue,
						Children: []*ast.ChildValue{
							{
								Name: gql.OR,
								Value: &ast.Value{
									Kind: ast.ListValue,
									Children: []*ast.ChildValue{
										{
											Value: &ast.Value{
												Kind: ast.ObjectValue,
												Children: []*ast.ChildValue{
													{
														Name: "name",
														Value: &ast.Value{
															Kind: ast.ObjectValue,
															Children: []*ast.ChildValue{
																{
																	Name: gql.I_LIKE,
																	Value: &ast.Value{
																		Kind: ast.StringValue,
																		Raw:  "%admin%",
																	},
																},
															},
														},
													},
												},
											},
										},
										{
											Value: &ast.Value{
												Kind: ast.ObjectValue,
												Children: []*ast.ChildValue{
													{
														Name: "email",
														Value: &ast.Value{
															Kind: ast.ObjectValue,
															Children: []*ast.ChildValue{
																{
																	Name: gql.REGEX,
																	Value: &ast.Value{
																		Kind: ast.StringValue,
																		Raw:  ".*@admin\\.com$",
																	},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expected: ` WHERE ("name" ILIKE $1 OR "email" ~ $2)`,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := compiler.NewContext(nil, dialect.Quotation(), nil)

			err := dialect.buildWhere(ctx, tt.args)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			result := formatSQL(ctx.String())
			expected := formatSQL(tt.expected)
			assert.Equal(t, expected, result)
		})
	}
}

func TestWhereBuilderErrors(t *testing.T) {
	dialect := &Dialect{}

	tests := []struct {
		name     string
		args     ast.ArgumentList
		wantErr  bool
		errorMsg string
	}{
		{
			name: "空的AND条件",
			args: ast.ArgumentList{
				{
					Name: gql.WHERE,
					Value: &ast.Value{
						Kind: ast.ObjectValue,
						Children: []*ast.ChildValue{
							{
								Name: gql.AND,
								Value: &ast.Value{
									Kind:     ast.ListValue,
									Children: []*ast.ChildValue{},
								},
							},
						},
					},
				},
			},
			wantErr:  true,
			errorMsg: "logical operator AND requires at least one condition",
		},
		{
			name: "空的OR条件",
			args: ast.ArgumentList{
				{
					Name: gql.WHERE,
					Value: &ast.Value{
						Kind: ast.ObjectValue,
						Children: []*ast.ChildValue{
							{
								Name: gql.OR,
								Value: &ast.Value{
									Kind:     ast.ListValue,
									Children: []*ast.ChildValue{},
								},
							},
						},
					},
				},
			},
			wantErr:  true,
			errorMsg: "logical operator OR requires at least one condition",
		},
		{
			name: "空的NOT条件",
			args: ast.ArgumentList{
				{
					Name: gql.WHERE,
					Value: &ast.Value{
						Kind: ast.ObjectValue,
						Children: []*ast.ChildValue{
							{
								Name:  gql.NOT,
								Value: nil,
							},
						},
					},
				},
			},
			wantErr:  true,
			errorMsg: "NOT operator requires a condition",
		},
		{
			name: "不支持的操作符",
			args: ast.ArgumentList{
				{
					Name: gql.WHERE,
					Value: &ast.Value{
						Kind: ast.ObjectValue,
						Children: []*ast.ChildValue{
							{
								Name: "field",
								Value: &ast.Value{
									Kind: ast.ObjectValue,
									Children: []*ast.ChildValue{
										{
											Name: "UNKNOWN_OP",
											Value: &ast.Value{
												Kind: ast.StringValue,
												Raw:  "value",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr:  true,
			errorMsg: "unsupported operator: UNKNOWN_OP",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := compiler.NewContext(nil, dialect.Quotation(), nil)

			err := dialect.buildWhere(ctx, tt.args)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
				return
			}

			assert.NoError(t, err)
		})
	}
}
