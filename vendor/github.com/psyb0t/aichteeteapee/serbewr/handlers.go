package serbewr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/aichteeteapee"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
)

// HealthHandler provides a basic health check endpoint.
func (s *Server) HealthHandler(
	w http.ResponseWriter,
	_ *http.Request,
) {
	response := map[string]any{
		"status":    "ok",
		"timestamp": time.Now().UTC(),
	}

	aichteeteapee.WriteJSON(
		w,
		http.StatusOK,
		response,
	)
}

// EchoHandler echoes back request information (useful for testing).
//
//nolint:funlen // Header filtering adds necessary length
func (s *Server) EchoHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Body != nil && r.ContentLength > 0 {
		if !aichteeteapee.IsRequestContentTypeJSON(r) {
			aichteeteapee.WriteJSON(
				w,
				http.StatusUnsupportedMediaType,
				aichteeteapee.ErrorResponseUnsupportedContentType,
			)

			return
		}
	}

	var body any

	if r.Body != nil {
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&body); err != nil {
			ctxscope.GetLogger(r.Context()).Warn(
				"failed to decode request body in echo handler",
				"reason", "invalid_json_body",
				"err", err,
			)
		}
	}

	canon := http.CanonicalHeaderKey

	sensitiveHeaders := map[string]struct{}{
		canon(aichteeteapee.HeaderNameAuthorization):      {},
		canon(aichteeteapee.HeaderNameCookie):             {},
		canon(aichteeteapee.HeaderNameSetCookie):          {},
		canon(aichteeteapee.HeaderNameXAPIKey):            {},
		canon(aichteeteapee.HeaderNameProxyAuthorization): {},
	}

	filteredHeaders := make(http.Header, len(r.Header))

	for k, v := range r.Header {
		if _, blocked := sensitiveHeaders[k]; blocked {
			continue
		}

		filteredHeaders[k] = v
	}

	response := map[string]any{
		"method":  r.Method,
		"path":    r.URL.Path,
		"query":   r.URL.Query(),
		"headers": filteredHeaders,
		"body":    body,
	}

	if user, ok := r.Context().Value(
		aichteeteapee.ContextKeyUser,
	).(string); ok {
		response["user"] = user
	}

	aichteeteapee.WriteJSON(
		w,
		http.StatusOK,
		response,
	)
}

// FileUploadPostprocessor defines the function signature for processing
// file upload responses.
type FileUploadPostprocessor func(
	response map[string]any,
	request *http.Request,
) (map[string]any, error)

type FilenamePrependType uint8

const (
	uploadFilePermissions = 0o600

	// FilenamePrependTypeNone does not add any prefix to the filename.
	FilenamePrependTypeNone FilenamePrependType = iota
	// FilenamePrependTypeDateTime prepends date and time in Y_M_D_H_I_S format.
	FilenamePrependTypeDateTime
	// FilenamePrependTypeUUID prepends a UUID4 to the filename (default).
	FilenamePrependTypeUUID
)

type FileUploadHandlerOption func(*FileUploadHandlerConfig)

// FileUploadHandlerConfig holds the configuration for the file upload handler.
type FileUploadHandlerConfig struct {
	postprocessor   FileUploadPostprocessor
	filenamePrepend FilenamePrependType
	maxSize         int64
}

// WithFileUploadHandlerPostprocessor sets a postprocessor that modifies
// the response.
func WithFileUploadHandlerPostprocessor(
	processor FileUploadPostprocessor,
) FileUploadHandlerOption {
	return func(config *FileUploadHandlerConfig) {
		config.postprocessor = processor
	}
}

// WithFilenamePrependType sets the type of prefix to add to uploaded filenames.
func WithFilenamePrependType(
	prependType FilenamePrependType,
) FileUploadHandlerOption {
	return func(config *FileUploadHandlerConfig) {
		config.filenamePrepend = prependType
	}
}

// WithFileUploadMaxSize sets the total multipart request limit in bytes.
// Non-positive values retain DefaultFileUploadMaxSize.
func WithFileUploadMaxSize(maxSize int64) FileUploadHandlerOption {
	return func(config *FileUploadHandlerConfig) {
		if maxSize > 0 {
			config.maxSize = maxSize
		}
	}
}

// FileUploadHandler returns a handler for file uploads to the specified
// directory.
func (s *Server) FileUploadHandler(
	uploadsDir string,
	opts ...FileUploadHandlerOption,
) http.HandlerFunc {
	config := &FileUploadHandlerConfig{
		filenamePrepend: FilenamePrependTypeUUID,
		maxSize:         aichteeteapee.DefaultFileUploadMaxSize,
	}
	for _, opt := range opts {
		opt(config)
	}

	const dirPermissions = 0o750
	if err := os.MkdirAll(uploadsDir, dirPermissions); err != nil {
		s.logger.Error(
			"failed to create uploads directory",
			"err", err,
		)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			aichteeteapee.WriteJSON(
				w,
				http.StatusMethodNotAllowed,
				aichteeteapee.ErrorResponseMethodNotAllowed,
			)

			return
		}

		if err := s.handleFileUpload(w, r, uploadsDir, config); err != nil {
			return
		}
	}
}

// handleFileUpload processes the file upload and writes the response
//
//nolint:funlen // Complex file upload logic requires length
func (s *Server) handleFileUpload(
	w http.ResponseWriter,
	r *http.Request,
	uploadsDir string,
	config *FileUploadHandlerConfig,
) error {
	r.Body = http.MaxBytesReader(w, r.Body, fileUploadMaxSize(config))

	if err := r.ParseMultipartForm(s.config.FileUploadMaxMemory); err != nil {
		ctxscope.GetLogger(r.Context()).Warn(
			"failed to parse multipart form",
			"reason", "invalid_multipart_form",
			"err", err,
		)
		aichteeteapee.WriteJSON(
			w,
			http.StatusBadRequest,
			aichteeteapee.ErrorResponseInvalidMultipartForm,
		)

		return ctxerrors.Wrap(err, "parse multipart form")
	}

	defer func(ctx context.Context) {
		if removeErr := r.MultipartForm.RemoveAll(); removeErr != nil {
			ctxscope.GetLogger(ctx).Warn(
				"failed to remove multipart temporary files",
				"err", removeErr,
			)
		}
	}(r.Context())

	file, handler, err := r.FormFile("file")
	if err != nil {
		ctxscope.GetLogger(r.Context()).Warn(
			"failed to get file from form",
			"reason", "missing_upload_file",
			"err", err,
		)
		aichteeteapee.WriteJSON(
			w,
			http.StatusBadRequest,
			aichteeteapee.ErrorResponseNoFileProvided,
		)

		return ctxerrors.Wrap(err, "get form file")
	}

	defer func(ctx context.Context) {
		if closeErr := file.Close(); closeErr != nil {
			ctxscope.GetLogger(ctx).Warn(
				"failed to close uploaded file",
				"err", closeErr,
			)
		}
	}(r.Context())

	uniqueFilename := s.generateUniqueFilename(
		handler.Filename, config.filenamePrepend,
	)
	filePath := filepath.Join(uploadsDir, uniqueFilename)

	if err := s.saveUploadedFile(r.Context(), file, filePath); err != nil {
		aichteeteapee.WriteJSON(
			w,
			http.StatusInternalServerError,
			aichteeteapee.ErrorResponseFileSaveFailed,
		)

		return ctxerrors.Wrap(err, "save uploaded file")
	}

	response := map[string]any{
		"status":            "success",
		"original_filename": handler.Filename,
		"saved_filename":    uniqueFilename,
		"size":              handler.Size,
		"path":              uniqueFilename,
	}

	if config.postprocessor != nil {
		processedResponse, err := config.postprocessor(response, r)
		if err != nil {
			ctxscope.GetLogger(r.Context()).Error(
				"failed to postprocess upload response",
				"err", err,
			)
			aichteeteapee.WriteJSON(
				w,
				http.StatusInternalServerError,
				aichteeteapee.ErrorResponseInternalServerError,
			)

			return ctxerrors.Wrap(err, "postprocess upload response")
		}

		response = processedResponse
	}

	aichteeteapee.WriteJSON(
		w,
		http.StatusOK,
		response,
	)

	return nil
}

func fileUploadMaxSize(config *FileUploadHandlerConfig) int64 {
	if config != nil && config.maxSize > 0 {
		return config.maxSize
	}

	return aichteeteapee.DefaultFileUploadMaxSize
}

// saveUploadedFile saves the uploaded file to the specified path.
func (s *Server) saveUploadedFile(
	ctx context.Context,
	src io.Reader,
	filePath string,
) error {
	dst, err := os.OpenFile(
		filePath,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		uploadFilePermissions,
	)
	if err != nil {
		ctxscope.GetLogger(ctx).Error(
			"failed to create destination file",
			"err", err,
		)

		return ctxerrors.Wrap(err, "create file")
	}

	defer func() {
		if closeErr := dst.Close(); closeErr != nil {
			ctxscope.GetLogger(ctx).Warn(
				"failed to close destination file",
				"err", closeErr,
			)
		}
	}()

	if _, err := io.Copy(dst, src); err != nil {
		ctxscope.GetLogger(ctx).Error(
			"failed to copy uploaded file content",
			"err", err,
		)

		return ctxerrors.Wrap(err, "copy uploaded file content")
	}

	return nil
}

// generateUniqueFilename creates a unique filename based on the prepend type.
func (s *Server) generateUniqueFilename(
	originalFilename string,
	prependType FilenamePrependType,
) string {
	originalFilename = filepath.Base(originalFilename)

	switch prependType {
	case FilenamePrependTypeNone:
		return originalFilename
	case FilenamePrependTypeDateTime:
		now := time.Now()
		dateTimePrefix := now.Format("2006_01_02_15_04_05")

		return fmt.Sprintf("%s_%s", dateTimePrefix, originalFilename)
	case FilenamePrependTypeUUID:
		fallthrough
	default:
		uniqueID := uuid.New().String()

		return fmt.Sprintf("%s_%s", uniqueID, originalFilename)
	}
}
