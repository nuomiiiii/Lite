package javascript

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/require"
	"github.com/nuomiiiii/lite/utils/messageSender/factory"
)

// scriptTimeout bounds one send. httpTimeout is shorter so a slow request
// fails inside the script instead of racing the outer deadline.
var (
	scriptTimeout = 30 * time.Second
	httpTimeout   = 20 * time.Second
)

type JavaScriptSender struct {
	Addition
	sendMu      sync.Mutex
	mu          sync.Mutex
	vm          *goja.Runtime
	noopProgram *goja.Program
	gen         uint64
}

func (j *JavaScriptSender) GetName() string {
	return "Javascript"
}

func (j *JavaScriptSender) GetConfiguration() factory.Configuration {
	return &j.Addition
}

func (j *JavaScriptSender) Init() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.initLocked()
}

func (j *JavaScriptSender) initLocked() error {
	if j.Addition.Script == "" {
		return errors.New("JavaScript script is empty")
	}

	// 创建 JavaScript 运行时
	j.vm = goja.New()

	// 预编译一个 no-op 程序,用于驱动微任务队列
	prog, errc := goja.Compile("noop.js", "void 0", false)
	if errc == nil {
		j.noopProgram = prog
	}

	// 设置 require 支持
	new(require.Registry).Enable(j.vm)

	// 注入全局对象和函数
	j.setupGlobals()

	// 加载用户脚本
	_, err := j.vm.RunString(j.Addition.Script)
	if err != nil {
		j.vm = nil
		return fmt.Errorf("failed to load JavaScript script: %v", err)
	}

	// 验证 sendMessage 函数是否存在
	sendMessage := j.vm.Get("sendMessage")
	if sendMessage == nil || goja.IsUndefined(sendMessage) {
		j.vm = nil
		return errors.New("sendMessage function not defined in script")
	}

	// 验证是否可调用
	if _, ok := goja.AssertFunction(sendMessage); !ok {
		j.vm = nil
		return errors.New("sendMessage is not a function")
	}

	// sendEvent 函数是可选的,不强制要求存在

	return nil
}

func (j *JavaScriptSender) Destroy() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.abandonLocked()
	return nil
}

func (j *JavaScriptSender) abandonLocked() {
	j.gen++
	j.vm = nil
	j.noopProgram = nil
}

func (j *JavaScriptSender) enqueue(gen uint64, fn func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.vm == nil || j.gen != gen {
		return
	}
	fn()
}

// runMicrotasks 安全地推动 goja 的微任务队列(例如 Promise 回调)
func (j *JavaScriptSender) runMicrotasks() {
	if j.vm == nil {
		return
	}
	if j.noopProgram != nil {
		_, _ = j.vm.RunProgram(j.noopProgram)
		return
	}
	// 兜底: 直接运行一段 no-op 代码
	_, _ = j.vm.RunString("void 0")
}

func (j *JavaScriptSender) setupGlobals() {
	// 注入 console.log
	console := j.vm.NewObject()
	console.Set("log", func(call goja.FunctionCall) goja.Value {
		var args []interface{}
		for _, arg := range call.Arguments {
			args = append(args, arg.Export())
		}
		fmt.Println(args...)
		return goja.Undefined()
	})
	console.Set("error", func(call goja.FunctionCall) goja.Value {
		fmt.Print("Error: ")
		for i, arg := range call.Arguments {
			if i > 0 {
				fmt.Print(" ")
			}
			fmt.Print(arg.Export())
		}
		fmt.Println()
		return goja.Undefined()
	})
	j.vm.Set("console", console)

	// 注入 fetch API
	j.vm.Set("fetch", j.createFetchFunction())

	// 注入 XMLHttpRequest (xhr)
	j.vm.Set("XMLHttpRequest", j.createXHRConstructor())

	// 注入 setTimeout
	j.vm.Set("setTimeout", func(call goja.FunctionCall) goja.Value {
		callback := call.Argument(0)
		delay := call.Argument(1).ToInteger()
		if delay < 0 {
			delay = 0
		}
		gen := j.gen
		go func() {
			time.Sleep(time.Duration(delay) * time.Millisecond)
			j.enqueue(gen, func() {
				if fn, ok := goja.AssertFunction(callback); ok {
					_, _ = fn(goja.Undefined())
				}
			})
		}()
		return goja.Undefined()
	})

	// 注入 Promise 构造函数
	j.vm.RunString(`
		if (typeof Promise === 'undefined') {
			// Promise polyfill 会由 goja 自动提供
		}
	`)
}

func init() {
	factory.RegisterMessageSender(func() factory.IMessageSender {
		return &JavaScriptSender{}
	})
}

// 确保实现了 IMessageSender 接口
var _ factory.IMessageSender = (*JavaScriptSender)(nil)
