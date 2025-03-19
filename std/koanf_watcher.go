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

// koanfWatcher 实现了ConfigWatcher接口，用于监视koanf配置文件变更
type koanfWatcher struct {
	configFile   string               // 配置文件路径
	koanf        *koanf.Koanf         // koanf实例
	watcher      *fsnotify.Watcher    // 文件监视器
	callbacks    []func(*koanf.Koanf) // 回调函数列表
	mu           sync.RWMutex         // 互斥锁
	stopChan     chan struct{}        // 停止信号通道
	options      []KoanfOption        // koanf配置选项
	debounceTime time.Duration        // 防抖时间
}

// NewConfigWatcher 创建配置文件监视器
func NewConfigWatcher(k *koanf.Koanf, configFile string, options ...KoanfOption) (ConfigWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("创建文件监视器失败: %w", err)
	}

	return &koanfWatcher{
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
func (w *koanfWatcher) Start() error {
	// 监视配置文件所在目录
	dir := filepath.Dir(w.configFile)
	if err := w.watcher.Add(dir); err != nil {
		return fmt.Errorf("添加监视目录失败: %w", err)
	}

	// 用于防抖动处理的定时器
	var debounceTimer *time.Timer
	var lastEvent time.Time

	// 启动监视协程
	go func() {
		for {
			select {
			case event, ok := <-w.watcher.Events:
				if !ok {
					return
				}

				// 检查是否是我们关注的配置文件
				if filepath.Base(event.Name) != filepath.Base(w.configFile) {
					continue
				}

				// 检查是否是写入或创建事件
				if !isWriteOrCreateOp(event.Op) {
					continue
				}

				// 防抖处理
				now := time.Now()
				if now.Sub(lastEvent) < w.debounceTime {
					if debounceTimer != nil {
						debounceTimer.Stop()
					}
				}
				lastEvent = now

				// 设置防抖定时器
				debounceTimer = time.AfterFunc(w.debounceTime, func() {
					w.reloadConfig()
				})

			case err, ok := <-w.watcher.Errors:
				if !ok {
					return
				}
				fmt.Printf("配置文件监视错误: %v\n", err)

			case <-w.stopChan:
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
func (w *koanfWatcher) reloadConfig() {
	// 创建新的配置实例
	newKoanf, err := NewKoanf(w.configFile, w.options...)
	if err != nil {
		fmt.Printf("重新加载配置失败: %v\n", err)
		return
	}

	// 调用回调函数
	w.mu.RLock()
	defer w.mu.RUnlock()

	for _, callback := range w.callbacks {
		callback(newKoanf)
	}

	// 更新koanf实例（注意：这里只更新了引用，实际使用时需要考虑并发安全性）
	w.koanf = newKoanf
}

// Stop 停止监视配置文件变更
func (w *koanfWatcher) Stop() {
	close(w.stopChan)
	w.watcher.Close()
}

// OnChange 设置配置文件变更回调函数
func (w *koanfWatcher) OnChange(callback func(*koanf.Koanf)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.callbacks = append(w.callbacks, callback)
}

// SetDebounceTime 设置配置文件变更防抖时间
func (w *koanfWatcher) SetDebounceTime(duration time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.debounceTime = duration
}
