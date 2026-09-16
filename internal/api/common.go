package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"reflect"
	"strings"
)

type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// decodeJSON reads exactly one JSON object from the request body into dst,
// enforcing the byte budget and rejecting unknown fields. On any violation it
// writes the error response itself and returns false; the caller can stop.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64, invalidMessage string) bool {
	body := http.MaxBytesReader(w, r.Body, maxBytes)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", invalidMessage)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求体只能包含一个 JSON 对象。")
		return false
	}
	return true
}

// sanitizeValue converts a response value into JSON-safe generic data: NaN/Inf
// floats become 0 instead of failing json.Marshal, and named integer types
// (e.g. ManaColor) keep their custom MarshalJSON.
func sanitizeValue(value reflect.Value) any {
	if !value.IsValid() {
		return nil
	}
	if value.CanInterface() {
		// Honor custom JSON encoders (ManaColor.MarshalJSON emits "W"/"U"/…) before
		// the reflective kind switch flattens a named integer type to its number.
		if marshaler, ok := value.Interface().(json.Marshaler); ok {
			if data, err := marshaler.MarshalJSON(); err == nil {
				var decoded any
				if json.Unmarshal(data, &decoded) == nil {
					return decoded
				}
			}
		}
	}
	if value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return sanitizeValue(value.Elem())
	}
	switch value.Kind() {
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return 0.0
		}
		return number
	case reflect.String:
		return value.String()
	case reflect.Bool:
		return value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return value.Uint()
	case reflect.Slice, reflect.Array:
		items := make([]any, value.Len())
		for i := 0; i < value.Len(); i++ {
			items[i] = sanitizeValue(value.Index(i))
		}
		return items
	case reflect.Map:
		result := make(map[string]any)
		iterator := value.MapRange()
		for iterator.Next() {
			result[fmt.Sprint(iterator.Key().Interface())] = sanitizeValue(iterator.Value())
		}
		return result
	case reflect.Struct:
		result := make(map[string]any)
		typ := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			result[name] = sanitizeValue(value.Field(i))
		}
		return result
	default:
		return nil
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(sanitizeValue(reflect.ValueOf(value)))
	if err != nil {
		slog.Error("encode response", "error", err)
		http.Error(w, `{"error":{"code":"ENCODING_FAILED","message":"Failed to encode response."}}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	response := errorResponse{}
	response.Error.Code = code
	response.Error.Message = message
	writeJSON(w, status, response)
}
