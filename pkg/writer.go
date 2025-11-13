package pkg

import (
	"fmt"
	"io"
	"net/http"
)

func writeAndFlush(writer io.Writer, format string, a ...any) {
	_, e := fmt.Fprintf(writer, format, a...)
	if e != nil {
		fmt.Println("failed to write to terminal", "stdout:", e)
	} else {
		if flush, ok := writer.(http.Flusher); ok {
			flush.Flush()
		}
	}
}
