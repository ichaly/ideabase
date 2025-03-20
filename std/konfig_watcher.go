package std

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/knadh/koanf/v2"
)

// ConfigWatcher 配置文件监视器接口
type ConfigWatcher interface {
	// Start 开始监视配置文件变更
	Start() error
	// Stop 停止监视配置文件变更
	Stop()
	// OnChange 设置配置文件变更回调函数
	OnChange(func(*koanf.Koanf))
	// SetDebounceTime 设置配置文件变更防抖时间
	SetDebounceTime(time.Duration)
}

// konfigWatcher 实现了ConfigWatcher接口，用于监视konfig配置文件变更
type konfigWatcher struct {
	configFile   string               // 配置文件路径
	koanf        *koanf.Koanf         // koanf实例
	watcher      *fsnotify.Watcher    // 文件监视器
	callbacks    []func(*koanf.Koanf) // 回调函数列表
	mu           sync.RWMutex         // 互斥锁
	stopChan     chan struct{}        // 停止信号通道
	options      []KonfigOption       // konfig配置选项
	debounceTime time.Duration        // 防抖时间
}

// NewConfigWatcher 创建配置文件监视器
func NewConfigWatcher(k *koanf.Koanf, configFile string, options ...KonfigOption) (ConfigWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("创建文件监视器失败: %w", err)
	}

	return &konfigWatcher{
		configFile:   configFile,
		koanf:        k,
		watcher:      w,
		callbacks:    make([]func(*koanf.Koanf), 0),
		stopChan:     make(chan struct{}),
		options:      options,
		debounceTime: 100 * time.Millisecond, // 默认防抖时间100毫秒
	}, nil
}

// Start 开始监视配置文件变更
func (my *konfigWatcher) Start() error {
	// 监视配置文件所在目录
	dir := filepath.Dir(my.configFile)
	if err := my.watcher.Add(dir); err != nil {
		return fmt.Errorf("添加监视目录失败: %w", err)
	}

	// 用于防抖动处理的定时器
	var debounceTimer *time.Timer
	var lastEvent time.Time

	// 启动监视协程
	go func() {
		for {
			select {
			case event, ok := <-my.watcher.Events:
				if !ok {
					return
				}

				// 检查是否是我们关注的配置文件
				if filepath.Base(event.Name) != filepath.Base(my.configFile) {
					continue
				}

				// 检查是否是写入或创建事件
				if !isWriteOrCreateOp(event.Op) {
					continue
				}

				// 防抖处理
				now := time.Now()
				if now.Sub(lastEvent) < my.debounceTime {
					if debounceTimer != nil {
						debounceTimer.Stop()
					}
				}
				lastEvent = now

				// 设置防抖定时器
				debounceTimer = time.AfterFunc(my.debounceTime, func() {
					my.reloadConfig()
				})

			case err, ok := <-my.watcher.Errors:
				if !ok {
					return
				}
				fmt.Printf("配置文件监视错误: %v\n", err)

			case <-my.stopChan:
				return
			}
		}
	}()

	return nil
}

// 检查是否是写入或创建操作
func isWriteOrCreateOp(op fsnotify.Op) bool {
	return op&fsnotify.Write == fsnotify.Write || op&fsnotify.Create == fsnotify.Create
}

// 重新加载配置
func (my *konfigWatcher) reloadConfig() {
	// 创建新的配置实例
	newKonfig, err := NewKonfig(my.configFile, my.options...)
	if err != nil {
		fmt.Printf("重新加载配置失败: %v\n", err)
		return
	}

	// 调用回调函数
	my.mu.RLock()
	defer my.mu.RUnlock()

	for _, callback := range my.callbacks {
		callback(newKonfig.GetKoanf())
	}

	// 更新koanf实例（注意：这里只更新了引用，实际使用时需要考虑并发安全性）
	my.koanf = newKonfig.GetKoanf()
}

// Stop 停止监视配置文件变更
func (my *konfigWatcher) Stop() {
	close(my.stopChan)
	my.watcher.Close()
}

// OnChange 设置配置文件变更回调函数
func (my *konfigWatcher) OnChange(callback func(*koanf.Koanf)) {
	my.mu.Lock()
	defer my.mu.Unlock()
	my.callbacks = append(my.callbacks, callback)
}

// SetDebounceTime 设置配置文件变更防抖时间
func (my *konfigWatcher) SetDebounceTime(duration time.Duration) {
	my.mu.Lock()
	defer my.mu.Unlock()
	my.debounceTime = duration
}
