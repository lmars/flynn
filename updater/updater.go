package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/flynn/flynn/Godeps/_workspace/src/gopkg.in/inconshreveable/log15.v2"
	"github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/discoverd/client"
)

var slugbuilderURI, slugrunnerURI string

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

var flynnApps = []string{
	"blobstore",
	"dashboard",
	"router",
	"gitreceive",
	"controller",
}

func run() error {
	log := log15.New()

	var images map[string]string
	if err := json.NewDecoder(os.Stdin).Decode(&images); err != nil {
		log.Error("error decoding images", "err", err)
		return err
	}

	instances, err := discoverd.GetInstances("flynn-controller", 10*time.Second)
	if err != nil {
		log.Error("error looking up controller in service discovery", "err", err)
		return err
	}
	client, err := controller.NewClient("", instances[0].Meta["AUTH_KEY"])
	if err != nil {
		log.Error("error creating controller client", "err", err)
		return err
	}

	log.Info("checking all images are present")
	uris := make(map[string]string, len(flynnApps)+2)
	for _, name := range append(flynnApps, "slugbuilder", "slugrunner") {
		var image string
		if name == "gitreceive" {
			image = "flynn/receiver"
		} else {
			image = "flynn/" + name
		}
		uri, ok := images[image]
		if !ok {
			err := fmt.Errorf("missing image: %s", image)
			log.Error(err.Error())
			return err
		}
		uris[name] = uri
	}
	slugbuilderURI = uris["slugbuilder"]
	slugrunnerURI = uris["slugrunner"]

	for _, name := range flynnApps {
		log := log.New("name", name)
		log.Info("deploying system app")

		app, err := client.GetApp(name)
		if err != nil {
			log.Error("error getting app", "err", err)
			return err
		}
		if err := deployApp(client, app, uris[name], log); err != nil {
			return err
		}
		log.Info("system app deployed")
	}

	apps, err := client.AppList()
	if err != nil {
		log.Error("error getting apps", "err", err)
		return err
	}
	for _, app := range apps {
		if app.System() {
			continue
		}
		log := log.New("name", app.Name)
		log.Info("deploying user app")
		// TODO: only deploy if the app uses slugrunner
		if err := deployApp(client, app, slugrunnerURI, log); err != nil {
			return err
		}
		log.Info("user app deployed")
	}
	return nil
}

func deployApp(client *controller.Client, app *ct.App, uri string, log log15.Logger) error {
	release, err := client.GetAppRelease(app.ID)
	if err != nil {
		log.Error("error getting release", "err", err)
		return err
	}
	artifact, err := client.GetArtifact(release.ArtifactID)
	if err != nil {
		log.Error("error getting release artifact", "err", err)
		return err
	}
	skipDeploy := artifact.URI == uri
	if app.Name == "gitreceive" {
		// deploy the gitreceive app if builder / runner images have changed
		proc, ok := release.Processes["app"]
		if !ok {
			e := "missing app process in gitreceive release"
			log.Error(e)
			return errors.New(e)
		}
		if proc.Env["SLUGBUILDER_IMAGE_URI"] != slugbuilderURI {
			proc.Env["SLUGBUILDER_IMAGE_URI"] = slugbuilderURI
			skipDeploy = false
		}
		if proc.Env["SLUGRUNNER_IMAGE_URI"] != slugrunnerURI {
			proc.Env["SLUGRUNNER_IMAGE_URI"] = slugrunnerURI
			skipDeploy = false
		}
		release.Processes["app"] = proc
	}
	if skipDeploy {
		log.Info("app up-to-date, nothing to do")
		return nil
	}
	artifact.ID = ""
	artifact.URI = uri
	if err := client.CreateArtifact(artifact); err != nil {
		log.Error("error creating artifact", "err", err)
		return err
	}
	release.ID = ""
	release.ArtifactID = artifact.ID
	if err := client.CreateRelease(release); err != nil {
		log.Error("error creating new release", "err", err)
		return err
	}
	if err := client.DeployAppRelease(app.ID, release.ID); err != nil {
		log.Error("error deploying app", "err", err)
		return err
	}
	return nil
}
