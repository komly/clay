package genhandler

import (
	"fmt"
	"go/build"
	"go/format"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/golang/glog"
	"github.com/golang/protobuf/proto"
	plugin "github.com/golang/protobuf/protoc-gen-go/plugin"
	"github.com/grpc-ecosystem/grpc-gateway/protoc-gen-grpc-gateway/descriptor"
	"google.golang.org/genproto/googleapis/api/annotations"
)

type Generator struct {
	options options
	reg     *descriptor.Registry
	imports []descriptor.GoPackage // common imports
}

// New returns a new generator which generates handler wrappers.
func New(reg *descriptor.Registry, opts ...Option) *Generator {
	o := options{}
	for _, opt := range opts {
		opt(&o)
	}
	g := &Generator{
		options: o,
		reg:     reg,
	}
	g.imports = append(g.imports,
		g.newGoPackage("context"),
		g.newGoPackage("github.com/pkg/errors"),
		g.newGoPackage("github.com/utrack/clay/v2/transport", "transport"),
	)
	return g
}

func (g *Generator) newGoPackage(pkgPath string, aalias ...string) descriptor.GoPackage {
	gopkg := descriptor.GoPackage{
		Path: pkgPath,
		Name: path.Base(pkgPath),
	}
	alias := gopkg.Name
	if len(aalias) > 0 {
		alias = aalias[0]
		gopkg.Alias = alias
	}

	reference := alias
	if reference == "" {
		reference = gopkg.Name
	}

	for i := 0; ; i++ {
		if err := g.reg.ReserveGoPackageAlias(alias, gopkg.Path); err == nil {
			break
		}
		alias = fmt.Sprintf("%s_%d", gopkg.Name, i)
		gopkg.Alias = alias
	}

	if pkg == nil {
		pkg = make(map[string]string)
	}
	pkg[reference] = alias

	return gopkg
}

func (g *Generator) Generate(targets []*descriptor.File) ([]*plugin.CodeGeneratorResponse_File, error) {
	var files []*plugin.CodeGeneratorResponse_File
	for _, file := range targets {
		glog.V(1).Infof("Processing %s", file.GetName())

		if len(file.Services) == 0 {
			glog.V(0).Infof("%s: %v", file.GetName(), errNoTargetService)
			continue
		}
		descCode, err := g.getDescTemplate(g.options.SwaggerDef[file.GetName()], file)

		if err != nil {
			return nil, err
		}
		formatted, err := format.Source([]byte(descCode))
		if err != nil {
			glog.Errorf("%v: %s", err, annotateString(descCode))
			return nil, err
		}
		name := filepath.Base(file.GetName())
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)

		goPkg := ""
		if file.GoPkg.Path != "." {
			goPkg = file.GoPkg.Path
		}
		output := fmt.Sprintf(filepath.Join(goPkg, "%s.pb.goclay.go"), base)
		output = filepath.Clean(output)

		files = append(files, &plugin.CodeGeneratorResponse_File{
			Name:    proto.String(output),
			Content: proto.String(string(formatted)),
		})
		glog.V(1).Infof("Will emit %s", output)

		if g.options.Impl {
			output := fmt.Sprintf(filepath.Join(goPkg, g.options.ImplPath, "%s.pb.impl.go"), base)
			output = filepath.Clean(output)

			if !g.options.Force && fileExists(output) {
				glog.V(0).Infof("Implementation will not be emitted: file '%s' already exists", output)
				continue
			}

			implCode, err := g.getImplTemplate(file)
			if err != nil {
				return nil, err
			}
			formatted, err := format.Source([]byte(implCode))
			if err != nil {
				glog.Errorf("%v: %s", err, annotateString(implCode))
				return nil, err
			}

			files = append(files, &plugin.CodeGeneratorResponse_File{
				Name:    proto.String(output),
				Content: proto.String(string(formatted)),
			})
			glog.V(1).Infof("Will emit %s", output)
		}
	}

	return files, nil
}

func (g *Generator) getDescTemplate(swagger []byte, f *descriptor.File) (string, error) {
	pkgSeen := make(map[string]bool)
	var imports []descriptor.GoPackage
	for _, pkg := range g.imports {
		pkgSeen[pkg.Path] = true
		imports = append(imports, pkg)
	}

	pkgs := []string{
		"fmt",
		"io/ioutil",
		"strings",
		"bytes",
		"net/http",

		"github.com/utrack/clay/v2/transport/httpruntime",
		"github.com/utrack/clay/v2/transport/httptransport",
		"github.com/utrack/clay/v2/transport/swagger",
		"github.com/grpc-ecosystem/grpc-gateway/runtime",
		"github.com/grpc-ecosystem/grpc-gateway/utilities",
		"google.golang.org/grpc",
		"github.com/go-chi/chi",
	}

	if swagger != nil {
		pkgs = append(pkgs, "github.com/go-openapi/spec")
	}

	for _, pkg := range pkgs {
		pkgSeen[pkg] = true
		imports = append(imports, g.newGoPackage(pkg))
	}

	httpmw := g.newGoPackage("github.com/utrack/clay/v2/transport/httpruntime/httpmw")
	httpcli := g.newGoPackage("github.com/utrack/clay/v2/transport/httpclient")
	for _, svc := range f.Services {
		for _, m := range svc.Methods {
			checkedAppend := func(pkg descriptor.GoPackage) {
				// Add request type package to imports if needed
				if m.Options == nil || !proto.HasExtension(m.Options, annotations.E_Http) ||
					pkg == f.GoPkg || pkgSeen[pkg.Path] {
					return
				}
				pkgSeen[pkg.Path] = true
				imports = append(imports, pkg)
			}

			checkedAppend(m.RequestType.File.GoPkg)
			checkedAppend(m.ResponseType.File.GoPkg)
		}

		if hasBindings(svc) && !pkgSeen[httpcli.Path] {
			imports = append(imports, httpcli)
			pkgSeen[httpcli.Path] = true
		}

		if g.options.ApplyDefaultMiddlewares && hasBindings(svc) && !pkgSeen[httpmw.Path] {
			imports = append(imports, httpmw)
			pkgSeen[httpmw.Path] = true
		}
	}

	p := param{File: f, Imports: imports,
		ApplyMiddlewares: g.options.ApplyDefaultMiddlewares,
	}

	if swagger != nil {
		p.SwaggerBuffer = swagger
	}
	return applyDescTemplate(p)
}

func (g *Generator) getImplTemplate(f *descriptor.File) (string, error) {
	pkgSeen := make(map[string]bool)
	var imports []descriptor.GoPackage
	for _, pkg := range g.imports {
		pkgSeen[pkg.Path] = true
		imports = append(imports, pkg)
	}
	for _, pkg := range []string{
		"context",
	} {
		pkgSeen[pkg] = true
		imports = append(imports, g.newGoPackage(pkg))
	}
	p := param{
		File:        f,
		CurrentPath: f.GoPkg.Path,
	}
	fileGoPkg := f.GoPkg
	if g.options.ImplPath != "" {
		rootImport := getRootImportPath(f)
		p.CurrentPath = filepath.Join(rootImport, g.options.ImplPath)
		f.GoPkg = g.newGoPackage(rootImport, "desc")
		f.GoPkg.Name = fileGoPkg.Name
		pkgSeen[f.GoPkg.Path] = true
		imports = append(imports, f.GoPkg)
	}

	for _, svc := range f.Services {
		for _, m := range svc.Methods {
			checkedAppend := func(pkg descriptor.GoPackage) {
				if m.Options == nil || !proto.HasExtension(m.Options, annotations.E_Http) ||
					pkg.Path == fileGoPkg.Path || pkgSeen[pkg.Path] {
					return
				}
				pkgSeen[pkg.Path] = true
				imports = append(imports, pkg)
			}
			checkedAppend(m.RequestType.File.GoPkg)
			checkedAppend(m.ResponseType.File.GoPkg)
		}
	}
	p.Imports = imports

	return applyImplTemplate(p)
}

func annotateString(str string) string {
	strs := strings.Split(str, "\n")
	for pos := range strs {
		strs[pos] = fmt.Sprintf("%v: %v", pos, strs[pos])
	}
	return strings.Join(strs, "\n")
}

func fileExists(path string) bool {
	dir, err := filepath.Abs(".")
	if err != nil {
		glog.V(-1).Info(err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		glog.V(-1).Info(err)
	}
	if _, err := os.Stat(filepath.Join(dir, path)); err == nil {
		return true
	}
	return false
}

func getRootImportPath(file *descriptor.File) string {
	goImportPath := ""
	if file.GoPkg.Path != "." {
		goImportPath = file.GoPkg.Path
	}
	// dir is current working directory
	dir, err := filepath.Abs(".")
	if err != nil {
		glog.V(-1).Info(err)
	}
	xdir, direrr := filepath.EvalSymlinks(dir)
	for _, gp := range strings.Split(build.Default.GOPATH, ":") {
		gp = filepath.Clean(gp)
		// xgp = gp but after symlink evaluation
		xgp, gperr := filepath.EvalSymlinks(gp)
		if strings.HasPrefix(dir, gp) {
			return getPackage(dir, gp, goImportPath)
		}
		if direrr == nil && strings.HasPrefix(xdir, gp) {
			return getPackage(xdir, gp, goImportPath)
		}
		if gperr == nil && strings.HasPrefix(dir, xgp) {
			return getPackage(dir, xgp, goImportPath)
		}
		if gperr == nil && direrr == nil && strings.HasPrefix(xdir, xgp) {
			return getPackage(xdir, xgp, goImportPath)
		}
	}
	return ""
}

func getPackage(path, gopath, gopkg string) string {
	currentPath := strings.TrimPrefix(path, filepath.Join(gopath, "src")+string(filepath.Separator))
	if strings.HasPrefix(gopkg, currentPath) {
		return gopkg
	} else if gopkg != "" {
		return filepath.Join(currentPath, gopkg)
	} else {
		return currentPath
	}
}

func hasBindings(service *descriptor.Service) bool {
	for _, m := range service.Methods {
		if len(m.Bindings) > 0 {
			return true
		}
	}
	return false
}
