// +build ignore

package main

import (
	"github.com/shurcooL/vfsgen"
	"net/http"
)

func main() {

	err := vfsgen.Generate(http.Dir("assets"), vfsgen.Options{PackageName: "listdb"})
	if err != nil {
		panic(err)
	}
}
