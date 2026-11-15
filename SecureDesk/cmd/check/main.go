// check runs bounded diagnostics from the same network namespace as the service.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"securedesk/internal/events"
	"securedesk/internal/platform"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var e error
	switch os.Args[1] {
	case "health":
		client := http.Client{Timeout: 5 * time.Second}
		var r *http.Response
		r, e = client.Get("http://127.0.0.1:8000/api/health")
		if e == nil {
			r.Body.Close()
			if r.StatusCode != 200 {
				e = fmt.Errorf("HTTP %d", r.StatusCode)
			}
		}
	case "tcp":
		if len(os.Args) != 3 {
			os.Exit(2)
		}
		fmt.Println(platform.TCP(os.Args[2]))
		return
	case "broker":
		c, _, err := events.Connect()
		e = err
		if e == nil {
			e = c.Close()
		}
	case "duplicate":
		if len(os.Args) != 3 {
			os.Exit(2)
		}
		e = events.Duplicate(ctx, os.Args[2])
	default:
		os.Exit(2)
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, "check failed")
		os.Exit(1)
	}
	fmt.Println("PASS")
}
