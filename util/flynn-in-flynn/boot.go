package main

import (
	"log"

	"github.com/flynn/flynn/test/cluster2"
)

func main() {
	_, err := cluster2.Boot(&cluster2.BootConfig{
		Size:         1,
		ImagesPath:   "build/images.json",
		ManifestPath: "build/manifests/bootstrap-manifest.json",
		KVM:          true,
	})
	if err != nil {
		log.Fatal(err)
	}
}
