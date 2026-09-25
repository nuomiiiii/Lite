package rpc

import "context"

func CallWithContext(ctx context.Context, id any, method string, params any) *JsonRpcResponse {
	if ctx == nil {
		ctx = context.Background()
	}
	req := &JsonRpcRequest{Version: RPC_VERSION, Method: method, Params: params, ID: id}
	if e := req.Validate(); e != nil {
		return ErrorResponse(id, e.Code, e.Message, e.Data)
	}
	muHandlers.RLock()
	h, ok := handlers[method]
	muHandlers.RUnlock()
	if !ok {
		return ErrorResponse(id, MethodNotFound, "method not found", method)
	}
	result, jerr := h(ctx, req)
	if jerr != nil {
		return ErrorResponse(id, jerr.Code, jerr.Message, jerr.Data)
	}
	return SuccessResponse(id, result)
}
