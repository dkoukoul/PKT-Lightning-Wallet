package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

func die(format string, args ...interface{}) {
	panic(fmt.Sprintf(format, args...))
}

func assertNil(x interface{}, format string, args ...interface{}) {
	if x != nil {
		die(format, args...)
	}
}

type exeF int

const (
	exeEcho       exeF = 1 << iota
	exeCanFail    exeF = 1 << iota
	exeNoRedirect exeF = 1 << iota
)

type execer []string

var noEnvExecer execer = []string{}

func exe(flags exeF, name string, arg ...string) (int, string, string) {
	return noEnvExecer.exe(flags, name, arg...)
}

func (e *execer) exe(flags exeF, name string, arg ...string) (int, string, string) {
	stdout := bytes.NewBuffer(nil)
	stderr := bytes.NewBuffer(nil)
	if flags&exeEcho != 0 {
		fmt.Println(strings.Join(append([]string{name}, arg...), " "))
	}
	cmd := exec.Command(name, arg...)
	if len(*e) > 0 {
		cmd.Env = append([]string{}, *e...)
	}
	if flags&exeNoRedirect == 0 {
		cmd.Stdout = stdout
		cmd.Stderr = stderr
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	err := cmd.Run()
	ret := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && flags&exeCanFail != 0 {
			ret = ee.ExitCode()
		} else {
			die("exe(%s, %v) -> %v", name, arg, err)
		}
	}
	return ret, stdout.String(), stderr.String()
}

func needUpdate(inPath string, outPaths []string) bool {
	if inFileStat, err := os.Stat(inPath); err != nil {
		panic(err)
	} else {
		for _, p := range outPaths {
			if fileStat, err := os.Stat(p); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					fmt.Printf("GENERATING   %s\n", p)
					return true
				} else {
					panic(err)
				}
			} else if inFileStat.ModTime().After(fileStat.ModTime()) {
				fmt.Printf("REGENERATING %s\n", p)
				return true
			}
		}
	}
	return false
}

func ensureDir(dir string) {
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			os.MkdirAll(dir, 0755)
		} else {
			panic(err)
		}
	}
}

func processProto(inPath string, e *execer) bool {
	inDir, file := filepath.Split(inPath)
	outDir := "./generated/" + inDir

	jsonfile := file + ".doc.json"
	jsonPath := filepath.Join(outDir, jsonfile)

	goFile := strings.Replace(file, ".proto", ".pb.go", -1)
	goPath := filepath.Join(outDir, goFile)

	ensureDir(outDir)
	if needUpdate(inPath, []string{jsonPath, goPath}) {
		e.exe(exeNoRedirect, "protoc",
			"-I.",

			"--pkt-json_opt=json,"+file+".doc.json",
			"--pkt-json_out="+outDir,

			"--pkt-go_opt=paths=source_relative",
			"--pkt-go_out=./generated/",

			"--pkt-grpc_opt=paths=source_relative",
			"--pkt-grpc_out=./generated/",

			inPath,
		)
		return true
	}
	return false
}

func writeFile(file, content string) {
	os.Remove(file)
	dir, _ := filepath.Split(file)
	if err := os.MkdirAll(dir, 0755); err != nil {
		panic(err)
	}
	os.WriteFile(file, []byte(content), 0664)
}
func writeVersion(file, version string) {
	writeFile(file, `
	package version
	func Version() string {
		return "`+version+`"
	}
	`)
}

func genproto(version string) {
	e := execer{"PATH=" + os.Getenv("PATH") + ":./builder/buildpkt/bin"}
	didUpdate := false
	err := filepath.Walk("proto", func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() && filepath.Ext(path) == ".proto" {
			didUpdate = processProto(path, &e) || didUpdate
		}
		return nil
	})
	if err != nil {
		panic(err)
	}
	if didUpdate {
		helpPath := "generated/pkthelp/"
		helpFile := helpPath + "helpgen.go"
		fmt.Printf("GENERATING   %s\n", helpFile)
		ensureDir(helpPath)
		_, out, err := e.exe(0, "./builder/buildpkt/bin/mkhelp", "./generated/proto/")
		if len(err) > 0 {
			fmt.Printf("mkhelp -> %s\n", err)
		}
		ioutil.WriteFile(helpFile, []byte(out), 0755)
	}
	writeVersion("generated/version/version.go", version)
	fmt.Println("Protobufs generated")
}

// build stuff

type config struct {
	buildargs []string
	bindir    string
}

func isWindowsBuild() bool {
	if os.Getenv("GOOS") == "windows" {
		return true
	} else if runtime.GOOS == "windows" && os.Getenv("GOOS") == "" {
		return true
	}
	return false
}

func build(name string, pkg string, conf *config) {
	if isWindowsBuild() {
		name = name + ".exe"
	}
	fmt.Printf("# Building %s\n", name)
	args := append([]string{"build", "-o", conf.bindir + "/" + name}, conf.buildargs...)
	args = append(args, pkg)
	exe(exeNoRedirect|exeEcho, "go", args...)
}

func chkdir() {
	info, err := os.Stat("./builder/buildpkt/bin/build")
	if err != nil || info.IsDir() {
		die("this script must be invoked from the project root")
	}
}

func buildStr() string {
	exe(0, "git", "update-index", "-q", "--refresh")
	_, id, _ := exe(0, "git", "describe", "--tags", "HEAD")
	id = strings.TrimSpace(id)
	if x, _, _ := exe(exeCanFail, "git", "diff", "--quiet"); x != 0 {
		if os.Getenv("PKT_FAIL_DIRTY") != "" {
			die("Build is dirty, aborting")
		}
		return id + "-dirty"
	}
	return id
}

func checkProtoc() {
	cmd := exec.Command("protoc", "--version")
	err := cmd.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "In order to compile pktd, you must have `protoc` installed\n")
		if runtime.GOOS == "darwin" {
			fmt.Fprintf(os.Stderr,
				"Since you seem to be using a Mac, you can use homebrew to install it\n"+
					"See: https://formulae.brew.sh/formula/protobuf\n",
			)
		} else if runtime.GOOS == "windows" {
			fmt.Fprintf(os.Stderr,
				"Since you seem to be using Windows, you can install it from github\n"+
					"See: https://www.geeksforgeeks.org/how-to-install-protocol-buffers-on-windows/\n",
			)
		} else if runtime.GOOS == "linux" {
			fmt.Fprintf(os.Stderr,
				"Since you seem to be using Linux, you can install it with your package manager\n"+
					"See: https://command-not-found.com/protoc\n",
			)
		}
		os.Exit(100)
	}
}

var regex = regexp.MustCompile("[A-Z0-9_]+=.*")

func setEnv(k, v string) {
	fmt.Printf("env %s=%s\n", k, v)
	os.Setenv(k, v)
}

func main() {
	chkdir()
	checkProtoc()
	genproto(buildStr())
	conf := config{}
	conf.bindir = "./bin"
	conf.buildargs = append(conf.buildargs, "-trimpath")

	assertNil(os.MkdirAll(conf.bindir, 0755), "mkdir bin")

	defaultEnv := make(map[string]string)
	defaultEnv["CGO_ENABLED"] = "0"

	for i, a := range os.Args {
		if i == 0 {
			continue
		}
		if !regex.MatchString(a) {
			conf.buildargs = append(conf.buildargs, a)
			continue
		}
		i := strings.IndexRune(a, '=')
		setEnv(a[0:i], a[i+1:])
		delete(defaultEnv, a[0:i])
	}
	for k, v := range defaultEnv {
		setEnv(k, v)
	}

	build("pld", "./lnd/cmd/lnd", &conf)
	build("pldctl", "./lnd/cmd/lndcli", &conf)
	fmt.Println("Everything looks good, type `./bin/pld --create` to make a wallet")
}
