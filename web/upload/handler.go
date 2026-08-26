package upload

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/web/api"
)

const maxChunkRequestSize = ChunkSize + 1*1024*1024

type Result struct {
	Message string
	Data    any
}

type Finalizer func(Session) (Result, error)
type OwnerResolver func(*gin.Context) string

type Handler struct {
	Store        *Store
	Finalizers   map[Purpose]Finalizer
	MaxSizes     map[Purpose]int64
	ResolveOwner OwnerResolver
}

func NewHandler(store *Store, owner OwnerResolver, finalizers map[Purpose]Finalizer, maxSizes map[Purpose]int64) *Handler {
	return &Handler{Store: store, ResolveOwner: owner, Finalizers: finalizers, MaxSizes: maxSizes}
}

func (h *Handler) Init(c *gin.Context) {
	var request struct {
		Purpose  Purpose `json:"purpose" binding:"required"`
		Size     int64   `json:"size" binding:"required"`
		Filename string  `json:"filename"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		api.RespondError(c, http.StatusBadRequest, fmt.Sprintf("invalid request: %v", err))
		return
	}
	if _, ok := h.Finalizers[request.Purpose]; !ok {
		api.RespondError(c, http.StatusBadRequest, "unsupported upload purpose")
		return
	}
	if request.Filename == "" {
		request.Filename = string(request.Purpose) + ".zip"
	}
	session, err := h.Store.Init(h.owner(c), request.Purpose, request.Filename, request.Size, h.MaxSizes[request.Purpose])
	if err != nil {
		h.respondUploadError(c, err)
		return
	}
	api.RespondSuccess(c, gin.H{
		"upload_id":  session.ID,
		"chunk_size": ChunkSize,
		"chunks":     chunkCount(session.Metadata.Size),
	})
}

func (h *Handler) Chunk(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxChunkRequestSize)
	uploadID := c.PostForm("upload_id")
	index, err := strconv.ParseInt(c.PostForm("chunk_index"), 10, 64)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, "chunk_index must be an integer")
		return
	}
	chunk, _, err := c.Request.FormFile("chunk_data")
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, fmt.Sprintf("get chunk data: %v", err))
		return
	}
	defer chunk.Close()
	if err := h.Store.SaveChunk(h.owner(c), uploadID, index, chunk); err != nil {
		h.respondUploadError(c, err)
		return
	}
	api.RespondSuccess(c, gin.H{"received": true, "chunk_index": index})
}

func (h *Handler) Merge(c *gin.Context) {
	var request struct {
		UploadID string `json:"upload_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		api.RespondError(c, http.StatusBadRequest, fmt.Sprintf("invalid request: %v", err))
		return
	}
	owner := h.owner(c)
	result, err := h.Store.MergeAndFinalize(owner, request.UploadID, func(session Session) (Result, error) {
		finalize, ok := h.Finalizers[session.Metadata.Purpose]
		if !ok {
			return Result{}, errors.New("unsupported upload purpose")
		}
		return finalize(session)
	})
	if err != nil {
		h.respondUploadError(c, err)
		return
	}
	api.RespondSuccessMessage(c, result.Message, result.Data)
}

func (h *Handler) Cancel(c *gin.Context) {
	var request struct {
		UploadID string `json:"upload_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		api.RespondError(c, http.StatusBadRequest, fmt.Sprintf("invalid request: %v", err))
		return
	}
	if err := h.Store.Cancel(h.owner(c), request.UploadID); err != nil {
		h.respondUploadError(c, err)
		return
	}
	api.RespondSuccess(c, gin.H{})
}

func (h *Handler) owner(c *gin.Context) string {
	if h.ResolveOwner == nil {
		return "default"
	}
	return h.ResolveOwner(c)
}

func (h *Handler) respondUploadError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrOwnerMismatch):
		api.RespondError(c, http.StatusNotFound, ErrNotFound.Error())
	case errors.Is(err, ErrTooManyUploads), errors.Is(err, ErrStorageLimit):
		api.RespondError(c, http.StatusConflict, err.Error())
	default:
		api.RespondError(c, http.StatusBadRequest, err.Error())
	}
}
