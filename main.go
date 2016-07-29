package main

import (
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	osUser "os/user"
	"path/filepath"
	"strings"
	"syscall"
)

func init() {
	log.SetFlags(log.Ltime | log.Lshortfile)
}

var (
	masterPort = flag.Int("master_port", 0, "Set flag to run a master at this port")
	masterAddr = flag.String("master", "", "Address of master to connect to")

	varStorage = flag.String("var_storage", "~/.rufs/", "Where to store some stuff")

	masterGenKeys = flag.Bool("master_gen_keys", false, "Generate keys for the master process")
	getAuthToken  = flag.String("get_auth_token", "", "Create auth token for this user")

	serverMods []func(s *Server) (module, error)
)

func getPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		usr, _ := osUser.Current()
		path = filepath.Join(usr.HomeDir, path[2:])
	}
	ex := strings.Split(path, "%rufs_var_storage%")
	for i := 1; len(ex) > i; i++ {
		ex[0] = filepath.Join(ex[0], getPath(*varStorage), ex[i])
	}
	return ex[0]
}

func ensureDirExists(dir string) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		panic(err)
	}
}

func registerServerModule(f func(s *Server) (module, error)) {
	serverMods = append(serverMods, f)
}

type module interface {
	Setup() error
	Run(done <-chan void) error
}

func main() {
	flag.Parse()
	log.Printf("Hello world")

	if *masterGenKeys {
		if err := genMasterKeys(); err != nil {
			log.Fatalln(err)
		}
		return
	}

	ret := make(chan error, 1)
	done := make(chan void)

	mods := []module{}

	if *masterPort != 0 || *getAuthToken != "" {
		m, err := newMaster(*masterPort)
		if err != nil {
			log.Fatalln(err)
		}
		mods = append(mods, m)
	}
	if *masterAddr != "" {
		m, err := newServer(*masterAddr)
		if err != nil {
			log.Fatalln(err)
		}
		mods = append(mods, m)

		for _, f := range serverMods {
			sm, err := f(m)
			if err != nil {
				log.Fatalln(err)
			}
			if sm == nil {
				continue
			}
			mods = append(mods, sm)
		}
	}

	if len(mods) == 0 {
		ret <- errors.New("You need to specify either --master_port or --master")
	}
	for _, m := range mods {
		if err := m.Setup(); err != nil {
			log.Fatalln(err)
		}
	}
	for _, m := range mods {
		go func(m module) {
			ret <- m.Run(done)
		}(m)
	}
	log.Printf("Launched %d modules...", len(mods))

	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch, syscall.SIGINT, syscall.SIGTERM)
	var err error
	select {
	case <-sigch:
		signal.Stop(sigch)
		close(done)
		err = <-ret
	case err = <-ret:
		close(done)
	}
	ec := 0
	for i := len(mods) - 1; i >= 0; i-- {
		if err != nil {
			log.Println(err)
			ec = 1
		}
		if i > 0 {
			err = <-ret
		}
	}
	os.Exit(ec)
}
