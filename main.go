package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/jyxjjj/OPSCFS/internal/storage"
)

func main() {
	root := flag.String("root", "./data", "storage root")
	keyHex := flag.String("key", "", "32-byte hex key")
	httpAddr := flag.String("http", "", "http listen addr, e.g. :8080")
	flag.Parse()
	if *keyHex == "" {
		fmt.Println("key is required")
		os.Exit(2)
	}
	key, err := hex.DecodeString(*keyHex)
	if err != nil {
		fmt.Println("invalid key:", err)
		os.Exit(2)
	}
	st, err := storage.New(*root, key)
	if err != nil {
		fmt.Println("storage init err:", err)
		os.Exit(2)
	}
	args := flag.Args()
	if *httpAddr != "" {
		// start gin server
		r := gin.Default()
		r.POST("/upload/:name", func(c *gin.Context) {
			name := c.Param("name")
			if name == "" {
				c.String(400, "name required")
				return
			}
			if err := st.Put(name, c.Request.Body); err != nil {
				c.String(500, err.Error())
				return
			}
			c.String(200, "ok")
		})
		r.GET("/download/:name", func(c *gin.Context) {
			name := c.Param("name")
			pr, pw := io.Pipe()
			go func() {
				defer pw.Close()
				if err := st.Get(name, pw); err != nil {
					pw.CloseWithError(err)
				}
			}()
			c.Status(200)
			io.Copy(c.Writer, pr)
		})
		r.Run(*httpAddr)
		return
	}
	if len(args) < 1 {
		fmt.Println("commands: add <name> <file>, get <name> <outfile>")
		os.Exit(2)
	}
	cmd := args[0]
	switch cmd {
	case "add":
		if len(args) < 3 {
			fmt.Println("add <name> <file>")
			os.Exit(2)
		}
		f, err := os.Open(args[2])
		if err != nil {
			fmt.Println(err)
			os.Exit(2)
		}
		defer f.Close()
		if err := st.Put(args[1], f); err != nil {
			fmt.Println(err)
			os.Exit(2)
		}
		fmt.Println("ok")
	case "get":
		if len(args) < 3 {
			fmt.Println("get <name> <outfile>")
			os.Exit(2)
		}
		out, err := os.Create(args[2])
		if err != nil {
			fmt.Println(err)
			os.Exit(2)
		}
		defer out.Close()
		if err := st.Get(args[1], out); err != nil {
			fmt.Println(err)
			os.Exit(2)
		}
		fmt.Println("ok")
	case "list":
		files := st.List()
		for name, manifest := range files {
			fmt.Printf("%s: %d blocks\n", name, len(manifest))
		}
	case "delete":
		if len(args) < 2 {
			fmt.Println("delete <name>")
			os.Exit(2)
		}
		if err := st.Delete(args[1]); err != nil {
			fmt.Println(err)
			os.Exit(2)
		}
		fmt.Println("ok")
	case "clean":
		if err := st.Clean(); err != nil {
			fmt.Println(err)
			os.Exit(2)
		}
		fmt.Println("ok")
	case "verify":
		if len(args) < 2 {
			fmt.Println("verify <name>")
			os.Exit(2)
		}
		name := args[1]
		err := st.Verify(name, func(done, total int) {
			fmt.Printf("verify %s: %d/%d\n", name, done, total)
		})
		if err != nil {
			fmt.Println("verify error:", err)
			os.Exit(2)
		}
		fmt.Println("ok")
	case "verify-all":
		files := st.List()
		for name := range files {
			fmt.Printf("verify-all: %s\n", name)
			if err := st.Verify(name, func(done, total int) {
				fmt.Printf("  %s: %d/%d\n", name, done, total)
			}); err != nil {
				fmt.Printf("verify-all %s error: %v\n", name, err)
			}
		}
	default:
		fmt.Println("unknown command")
	}
}
