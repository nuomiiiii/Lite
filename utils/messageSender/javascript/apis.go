package javascript

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/dop251/goja"
	"github.com/nuomiiiii/lite/utils/messageSender/outboundhttp"
)

// createFetchFunction 创建一个 fetch API 实现
func (j *JavaScriptSender) createFetchFunction() func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			panic(j.vm.NewTypeError("fetch requires at least 1 argument"))
		}

		url := call.Argument(0).String()

		// 解析选项
		options := map[string]interface{}{
			"method":  "GET",
			"headers": make(map[string]string),
			"body":    "",
		}

		if len(call.Arguments) > 1 {
			optObj := call.Argument(1).ToObject(j.vm)
			if optObj != nil {
				if method := optObj.Get("method"); method != nil && !goja.IsUndefined(method) {
					options["method"] = method.String()
				}
				if headers := optObj.Get("headers"); headers != nil && !goja.IsUndefined(headers) {
					headersObj := headers.ToObject(j.vm)
					if headersObj != nil {
						headerMap := make(map[string]string)
						for _, key := range headersObj.Keys() {
							headerMap[key] = headersObj.Get(key).String()
						}
						options["headers"] = headerMap
					}
				}
				if body := optObj.Get("body"); body != nil && !goja.IsUndefined(body) {
					options["body"] = body.String()
				}
			}
		}

		// 创建 Promise
		promise, resolve, reject := j.vm.NewPromise()
		gen := j.gen
		method := options["method"].(string)
		requestBody := options["body"].(string)
		headers := options["headers"].(map[string]string)

		go func() {
			fail := func(message string) {
				j.enqueue(gen, func() {
					reject(j.vm.ToValue(message))
				})
			}
			defer func() {
				if recovered := recover(); recovered != nil {
					fail(fmt.Sprintf("fetch panic: %v", recovered))
				}
			}()

			var body io.Reader
			if requestBody != "" {
				body = strings.NewReader(requestBody)
			}
			ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, method, url, body)
			if err != nil {
				fail(fmt.Sprintf("Failed to create request: %v", err))
				return
			}
			for key, value := range headers {
				req.Header.Set(key, value)
			}

			client := outboundhttp.NewClient(httpTimeout)
			resp, err := client.Do(req)
			if err != nil {
				fail(fmt.Sprintf("Fetch failed: %v", err))
				return
			}
			defer resp.Body.Close()
			bodyBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				fail(fmt.Sprintf("Failed to read response: %v", err))
				return
			}

			statusCode := resp.StatusCode
			statusText := resp.Status
			headerCopy := map[string]string{}
			for key, values := range resp.Header {
				if len(values) > 0 {
					headerCopy[key] = values[0]
				}
			}
			j.enqueue(gen, func() {
				responseObj := j.vm.NewObject()
				responseObj.Set("status", statusCode)
				responseObj.Set("statusText", statusText)
				responseObj.Set("ok", statusCode >= 200 && statusCode < 300)
				headersObj := j.vm.NewObject()
				for key, value := range headerCopy {
					headersObj.Set(key, value)
				}
				responseObj.Set("headers", headersObj)
				responseObj.Set("text", func(goja.FunctionCall) goja.Value {
					textPromise, textResolve, _ := j.vm.NewPromise()
					textResolve(j.vm.ToValue(string(bodyBytes)))
					return j.vm.ToValue(textPromise)
				})
				responseObj.Set("json", func(goja.FunctionCall) goja.Value {
					jsonPromise, jsonResolve, jsonReject := j.vm.NewPromise()
					var result interface{}
					if err := json.Unmarshal(bodyBytes, &result); err != nil {
						jsonReject(j.vm.ToValue(fmt.Sprintf("Failed to parse JSON: %v", err)))
					} else {
						jsonResolve(j.vm.ToValue(result))
					}
					return j.vm.ToValue(jsonPromise)
				})
				resolve(responseObj)
			})
		}()

		return j.vm.ToValue(promise)
	}
}

// createXHRConstructor 创建一个 XMLHttpRequest 构造函数
func (j *JavaScriptSender) createXHRConstructor() func(goja.ConstructorCall) *goja.Object {
	return func(call goja.ConstructorCall) *goja.Object {
		xhr := call.This

		// 内部状态
		var method, url string
		var headers = make(map[string]string)
		var requestBody string
		var async = true

		// readyState
		xhr.Set("readyState", 0)
		xhr.Set("status", 0)
		xhr.Set("statusText", "")
		xhr.Set("responseText", "")
		xhr.Set("response", "")

		// 事件处理器
		xhr.Set("onreadystatechange", goja.Null())
		xhr.Set("onload", goja.Null())
		xhr.Set("onerror", goja.Null())

		// open 方法
		xhr.Set("open", func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) < 2 {
				panic(j.vm.NewTypeError("open requires at least 2 arguments"))
			}
			method = call.Argument(0).String()
			url = call.Argument(1).String()
			if len(call.Arguments) > 2 {
				async = call.Argument(2).ToBoolean()
			}
			xhr.Set("readyState", 1)
			j.callHandler(xhr, "onreadystatechange")
			return goja.Undefined()
		})

		// setRequestHeader 方法
		xhr.Set("setRequestHeader", func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) < 2 {
				panic(j.vm.NewTypeError("setRequestHeader requires 2 arguments"))
			}
			key := call.Argument(0).String()
			value := call.Argument(1).String()
			headers[key] = value
			return goja.Undefined()
		})

		// send 方法
		xhr.Set("send", func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) > 0 && !goja.IsUndefined(call.Argument(0)) && !goja.IsNull(call.Argument(0)) {
				requestBody = call.Argument(0).String()
			}
			methodCopy := method
			urlCopy := url
			bodyCopy := requestBody
			headerCopy := make(map[string]string, len(headers))
			for key, value := range headers {
				headerCopy[key] = value
			}
			apply := func(status int, statusText, responseText string, failed bool) {
				xhr.Set("readyState", 4)
				xhr.Set("status", status)
				xhr.Set("statusText", statusText)
				xhr.Set("responseText", responseText)
				xhr.Set("response", responseText)
				if failed {
					j.callHandler(xhr, "onerror")
				} else {
					j.callHandler(xhr, "onload")
				}
				j.callHandler(xhr, "onreadystatechange")
			}
			doRequest := func() (int, string, string, bool) {
				var body io.Reader
				if bodyCopy != "" {
					body = bytes.NewReader([]byte(bodyCopy))
				}
				ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
				defer cancel()
				req, err := http.NewRequestWithContext(ctx, methodCopy, urlCopy, body)
				if err != nil {
					return 0, err.Error(), "", true
				}
				for key, value := range headerCopy {
					req.Header.Set(key, value)
				}
				client := outboundhttp.NewClient(httpTimeout)
				resp, err := client.Do(req)
				if err != nil {
					return 0, err.Error(), "", true
				}
				defer resp.Body.Close()
				bodyBytes, err := io.ReadAll(resp.Body)
				if err != nil {
					return resp.StatusCode, err.Error(), "", true
				}
				return resp.StatusCode, resp.Status, string(bodyBytes), false
			}
			if async {
				gen := j.gen
				go func() {
					defer func() {
						if recovered := recover(); recovered != nil {
							message := fmt.Sprintf("Error: %v", recovered)
							j.enqueue(gen, func() { apply(0, message, "", true) })
						}
					}()
					status, statusText, responseText, failed := doRequest()
					j.enqueue(gen, func() { apply(status, statusText, responseText, failed) })
				}()
			} else {
				status, statusText, responseText, failed := doRequest()
				apply(status, statusText, responseText, failed)
			}
			return goja.Undefined()
		})

		// getAllResponseHeaders 方法
		xhr.Set("getAllResponseHeaders", func(call goja.FunctionCall) goja.Value {
			return j.vm.ToValue("")
		})

		// getResponseHeader 方法
		xhr.Set("getResponseHeader", func(call goja.FunctionCall) goja.Value {
			return goja.Null()
		})

		return nil
	}
}

// callHandler 调用事件处理器
func (j *JavaScriptSender) callHandler(obj *goja.Object, handlerName string) {
	handler := obj.Get(handlerName)
	if handler != nil && !goja.IsUndefined(handler) && !goja.IsNull(handler) {
		if fn, ok := goja.AssertFunction(handler); ok {
			fn(obj)
		}
	}
}
