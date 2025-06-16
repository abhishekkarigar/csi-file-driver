package main

import (
	"github.com/abhishekkarigar/csi-fs-driver/driver"
	"log"
)

func main() {
	d, err := driver.NewDriver("csi.fs.local", "1.0.0", "/var/lib/kubelet/plugins/csi.fs.local")
	if err != nil {
		log.Fatalf("failed to start driver: %v", err)
	}
	d.Run()
}
