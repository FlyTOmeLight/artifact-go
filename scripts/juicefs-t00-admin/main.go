package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	m "github.com/minio/madmin-go/v2"
)

func main() {
	var p struct {
		Root, Secret, User, Password, Policy, Operation string
		Document                                        json.RawMessage
	}
	if json.NewDecoder(os.Stdin).Decode(&p) != nil {
		os.Exit(2)
	}
	c, e := m.New("127.0.0.1:19010", p.Root, p.Secret, false)
	if e != nil {
		os.Exit(3)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch p.Operation {
	case "create":
		e = c.AddUser(ctx, p.User, p.Password)
	case "policy":
		e = c.AddCannedPolicy(ctx, p.Policy, p.Document)
	case "attach":
		e = c.SetPolicy(ctx, p.Policy, p.User, false)
	case "delete-user":
		e = c.RemoveUser(ctx, p.User)
	case "delete-policy":
		e = c.RemoveCannedPolicy(ctx, p.Policy)
	default:
		os.Exit(2)
	}
	if e != nil {
		fmt.Printf("%T: %s\n", e, e.Error())
		os.Exit(1)
	}
	fmt.Println("ok")
}
