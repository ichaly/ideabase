// 全文搜索能力探测：按数据库驱动注册（与CDC唤醒源同款自注册模式）
// 新数据库实现探测函数并registerSearchDetector注册，执行器零修改
package gql

import "gorm.io/gorm"

// searchDetectors 搜索能力探测器注册表（key=gorm驱动名），返回(模式,分词配置)
var searchDetectors = map[string]func(db *gorm.DB) (string, string){}

// registerSearchDetector 注册搜索能力探测器
func registerSearchDetector(driver string, detect func(db *gorm.DB) (string, string)) {
	searchDetectors[driver] = detect
}

func init() {
	registerSearchDetector("postgres", detectPostgresSearch)
}

// detectPostgresSearch 探测PostgreSQL全文搜索能力：
// 1. 存在挂在jieba/zhparser解析器上的分词配置则tsvector（中文真分词）；
// 2. pg_trgm可用（尝试自动创建，contrib模块官方镜像自带）则trigram；3. 否则ilike降级
func detectPostgresSearch(db *gorm.DB) (string, string) {
	var config string
	if err := db.Raw(`SELECT c.cfgname FROM pg_ts_config c
		JOIN pg_ts_parser p ON c.cfgparser = p.oid
		WHERE p.prsname IN ('jieba', 'zhparser') LIMIT 1`).Scan(&config).Error; err == nil && config != "" {
		return "tsvector", config
	}

	// 先探测已安装（常态），缺失才尝试创建（权限不足时静默降级）
	probe := func() bool {
		var trigram int
		err := db.Raw(`SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm'`).Scan(&trigram).Error
		return err == nil && trigram == 1
	}
	if probe() {
		return "trigram", ""
	}
	db.Exec(`CREATE EXTENSION IF NOT EXISTS pg_trgm`)
	if probe() {
		return "trigram", ""
	}
	return "ilike", ""
}
