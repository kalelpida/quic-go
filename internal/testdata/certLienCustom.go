// +build CUSTOM

package testdata

import (
    "fmt"
    "io/ioutil"
    "path"
)

func init() {
    content, err := ioutil.ReadFile("lienVersCerts.txt")
    if err != nil {
        fmt.Println("Err : place in same directory as exe a file named lienVersCerts.txt")
    }
    CertPath = path.Dir(string(content)) 
}
