package apiv1

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"reflect"
	"strings"

	"github.com/gorilla/mux"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/pkt-cash/pktd/btcutil/er"
	"github.com/pkt-cash/pktd/btcutil/event"
	"github.com/pkt-cash/pktd/btcutil/lock"
	"github.com/pkt-cash/pktd/btcutil/util"
	"github.com/pkt-cash/pktd/generated/pkthelp"
	"github.com/pkt-cash/pktd/generated/proto/restrpc_pb/help_pb"
	"github.com/pkt-cash/pktd/generated/proto/rpc_pb"
	"github.com/pkt-cash/pktd/pktlog/log"
)

const _api_v1_ = "/api/v1/"

// Functions

func respondError(res http.ResponseWriter, code int, msg string) er.R {
	res.Header().Set("Content-Type", "text/plain")
	http.Error(res, msg, code)
	return nil
}

func parentCat(cat string) string {
	idx := strings.LastIndex(cat, "/")
	if idx < 0 {
		return ""
	}
	return cat[:idx]
}

func catName(cat string) string {
	return cat[strings.LastIndex(cat, "/")+1:]
}

func epName(epPath string, cat string) string {
	out := strings.Replace(epPath, cat, "", 1)
	if len(out) > 0 && out[0] == '/' {
		out = out[1:]
	}
	return out
}

func typeOf[T any]() reflect.Type {
	return reflect.TypeOf((*T)(nil)).Elem()
}

func toPm[T proto.Message]() proto.Message {
	return reflect.New(typeOf[T]().Elem()).Interface().(proto.Message)
}

//	convert	pkthelp.type to REST help proto struct
func convertHelpType(t pkthelp.Type) *help_pb.Type {
	resultType := &help_pb.Type{
		Name:        t.Name,
		Description: t.Description,
	}

	//	convert the array of fields
	for _, field := range t.Fields {
		resultType.Fields = append(resultType.Fields, &help_pb.Field{
			Name:        field.Name,
			Description: field.Description,
			Repeated:    field.Repeated,
			Type:        convertHelpType(field.Type),
		})
	}

	return resultType
}

func trimSplit(s string) []string {
	return util.Filter(
		util.Map(strings.Split(s, "\n"), strings.TrimSpace),
		func(s string) bool { return s != "" },
	)
}

func unmarshal(r *http.Request, m proto.Message, isJson bool) er.R {
	if isJson {
		if b, err := ioutil.ReadAll(r.Body); err != nil {
			return er.E(err)
		} else {
			if err := protojson.Unmarshal(b, m); err != nil {
				return er.E(err)
			}
		}
	} else {
		if b, err := io.ReadAll(r.Body); err != nil {
			return er.E(err)
		} else if err := proto.Unmarshal(b, m); err != nil {
			return er.E(err)
		}
	}
	return nil
}
func marshal(w http.ResponseWriter, m proto.Message, isJson bool) er.R {
	if m == nil {
		return nil
	}
	if isJson {
		marshaler := protojson.MarshalOptions{
			Multiline:       true,
			EmitUnpopulated: true,
			UseEnumNumbers:  false,
			Indent:          "\t",
		}
		if b, err := marshaler.Marshal(m); err != nil {
			return er.E(err)
		} else if _, err := w.Write(b); err != nil {
			return er.E(err)
		}
	} else {
		if b, err := proto.Marshal(m); err != nil {
			return er.E(err)
		} else if _, err := w.Write(b); err != nil {
			return er.E(err)
		}
	}
	return nil
}

// Endpoint

type endpoint struct {
	path     string
	category string
	mkReq    func() proto.Message
	helpRes  help_pb.EndpointHelp
	f        func(m proto.Message) (proto.Message, er.R)
}

func (e *endpoint) serveHttpOrErr(w http.ResponseWriter, r *http.Request, isJson bool) er.R {

	//	command URI handler
	req := e.mkReq()

	if r.Method != "POST" && r.Method != "GET" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return er.New("405 - Method not allowed: " + r.Method)
	}

	// There is actually a req struct, it's not Null.
	if _, ok := req.(*rpc_pb.Null); !ok {
		//	check the method again, because if it's a GET, there's no request payload
		if r.Method == "POST" {
			if err := unmarshal(r, req, isJson); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return err
			}
		} else if !util.Contains(e.helpRes.Features, help_pb.F_ALLOW_GET) {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return er.New("405 - Request should be a POST because the endpoint requires input")
		}
	}
	if res, err := e.f(req); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return err
	} else if err := marshal(w, res, isJson); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return err
	}
	return nil
}

func (e *endpoint) serveHTTP(w http.ResponseWriter, r *http.Request) er.R {
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	isJson := strings.Contains(ct, "application/json")
	if !isJson && !strings.Contains(ct, "application/protobuf") {
		if r.Method == "GET" {
			isJson = true
		} else {
			w.Header().Set("Connection", "close")
			w.Header().Set("Content-Type", "text/plain")
			http.Error(w, "415 - Invalid content type, must be json or protobuf", http.StatusUnsupportedMediaType)
			return nil
		}
	}
	if err := e.serveHttpOrErr(w, r, isJson); err != nil {
		if err = marshal(w, &rpc_pb.RestError{
			Message: err.Message(),
			Stack:   err.Stack(),
		}, isJson); err != nil {
			log.Errorf("Error replying to request for [%s] from [%s] - error sending error, giving up: [%s]",
				r.RequestURI, r.RemoteAddr, err)
		}
	}
	return nil
}

func (e *endpoint) respondHelp(res http.ResponseWriter, r *http.Request) er.R {
	he := endpoint{
		path:  e.path,
		mkReq: toPm[*rpc_pb.Null],
		f: func(m proto.Message) (proto.Message, er.R) {
			return &e.helpRes, nil
		},
	}
	return he.serveHTTP(res, r)
}

// Apiv1

type apiInt struct {
	cats  lock.GenRwLock[map[string][]string]
	funcs lock.GenRwLock[map[string]*endpoint]
}

type Apiv1 struct {
	internal *apiInt
	category string
}

func (a *Apiv1) cat(path string, description *string) *Apiv1 {
	if len(path) == 0 {
		panic("Cannot define category with no path")
	}
	if path[0] == '/' {
		panic("Path must not begin with a /")
	}
	if path[len(path)-1] == '/' {
		panic("Path must not end with a /")
	}
	if a.category != "" {
		path = a.category + "/" + path
	}
	a.internal.cats.W().In(func(cats *map[string][]string) er.R {
		if description == nil {
			if _, ok := (*cats)[path]; !ok {
				log.Warnf("No such defined category [%s]", path)
			}
		} else {
			if _, ok := (*cats)[path]; ok {
				log.Warnf("Creating category [%s] but it already exists")
			}
			(*cats)[path] = trimSplit(*description)
		}
		return nil
	})
	return &Apiv1{
		internal: a.internal,
		category: path,
	}
}

func (a *Apiv1) Category(path string) *Apiv1 {
	return a.cat(path, nil)
}

func DefineCategory(
	a *Apiv1,
	path string,
	description string,
) *Apiv1 {
	return a.cat(path, &description)
}

func Stream[Q proto.Message, R proto.Message](
	a *Apiv1,
	name string,
	description string,
	ev *event.Emitter[R],
	filter func(req Q) (func(R) bool, er.R),
	features ...help_pb.F,
) {

}

func Endpoint[Q proto.Message, R proto.Message](
	a *Apiv1,
	name string,
	description string,
	f func(req Q) (R, er.R),
	features ...help_pb.F,
) {
	path := name
	if a.category != "" {
		if path != "" {
			path = a.category + "/" + path
		} else {
			path = a.category
		}
	}

	// If no stability, set stability to EXPERIMENTAL
	hasStability := false
	for _, f := range features {
		switch f {
		case help_pb.F_DEPRECATED:
			fallthrough
		case help_pb.F_EXPERIMENTAL:
			fallthrough
		case help_pb.F_STABLE:
			fallthrough
		case help_pb.F_UNSTABLE:
			if hasStability {
				log.Warnf("Error registering RPC [%s]: [%s]", path,
					"Stability has been specified more than once")
			}
			hasStability = true
		}
	}
	if !hasStability {
		// No defined stability = experimental
		features = append(features, help_pb.F_EXPERIMENTAL)
	}

	// We're not going to return an error from here because
	// nobody wants to handle runtime errors while setting up
	// RPCs, in principle it should just panic, but we'll be nice
	// and print a warning only.
	log.Infof("Registering RPC [%s]", path)
	req := toPm[Q]()
	reqHt, err := pkthelp.Help(req)
	if err != nil {
		log.Warnf("Error registering RPC [%s]: [%s]", path, err)
		return
	}
	res := toPm[R]()
	resHt, err := pkthelp.Help(res)
	if err != nil {
		log.Warnf("Error registering RPC [%s]: [%s]", path, err)
		return
	}
	a.internal.funcs.W().In(func(funcs *map[string]*endpoint) er.R {
		(*funcs)[path] = &endpoint{
			path:     path,
			mkReq:    toPm[Q],
			category: a.category,
			helpRes: help_pb.EndpointHelp{
				Path:        _api_v1_ + path,
				Description: trimSplit(description),
				Request:     convertHelpType(reqHt),
				Response:    convertHelpType(resHt),
				Features:    features,
			},
			f: func(m proto.Message) (proto.Message, er.R) {
				if query, ok := m.(Q); !ok {
					panic("invalid type")
				} else {
					return f(query)
				}
			},
		}
		return nil
	})
}

func Deregister(a *Apiv1, path string) er.R {
	return a.internal.funcs.W().In(func(eps *map[string]*endpoint) er.R {
		_, ok := (*eps)[path]
		if !ok {
			return er.New("not found")
		}
		delete(*eps, path)
		return nil
	})
}

type epInfo struct {
	shortDesc string
	category  string
	name      string
	helpPath  string
}

func (a *Apiv1) masterHelp() (*help_pb.Category, er.R) {
	var eps []epInfo
	a.internal.funcs.R().In(func(t *map[string]*endpoint) er.R {
		for _, ep := range *t {
			eps = append(eps, epInfo{
				shortDesc: util.Iff(
					len(ep.helpRes.Description) > 0,
					func() string { return ep.helpRes.Description[0] },
					"<UNDEFINED>",
				),
				category: ep.category,
				name:     epName(ep.path, ep.category),
				helpPath: _api_v1_ + "help/" + ep.path,
			})
		}
		return nil
	})
	cats := make(map[string][]string)
	a.internal.cats.R().In(func(t *map[string][]string) er.R {
		for k, v := range *t {
			cats[k] = v
		}
		return nil
	})
	res := help_pb.Category{
		Description: []string{"PLD - The PKT Lightning Daemon REST interface"},
		Categories:  make(map[string]*help_pb.Category),
		Endpoints:   make(map[string]*help_pb.EndpointSimple),
	}
	categorized := make(map[string]*help_pb.Category)
	categorized[""] = &res
	for len(cats) > 0 {
		forwardProgress := false
		for cat, desc := range cats {
			if pcat, ok := categorized[parentCat(cat)]; ok {
				hcat := &help_pb.Category{
					Description: desc,
					Categories:  make(map[string]*help_pb.Category),
					Endpoints:   make(map[string]*help_pb.EndpointSimple),
				}
				pcat.Categories[catName(cat)] = hcat
				categorized[cat] = hcat
				delete(cats, cat)
				forwardProgress = true
			}
		}
		if !forwardProgress {
			log.Warnf("categories which cannot be categorized: [%v]", cats)
			break
		}
	}
	for _, epi := range eps {
		if cat, ok := categorized[epi.category]; ok {
			cat.Endpoints[epi.name] = &help_pb.EndpointSimple{
				HelpPath: epi.helpPath,
				Brief:    epi.shortDesc,
			}
		} else {
			log.Warnf("Uncategorized endpoint: [%s // %s]", epi.category, epi.name)
		}
	}
	return &res, nil
}

func send404(res http.ResponseWriter, path string) er.R {
	return respondError(res,
		404,
		fmt.Sprintf(
			"No such endpoint [%s], use /api/v1/help to see the full list",
			path,
		),
	)
}

func New() (*Apiv1, *mux.Router) {
	r := mux.NewRouter()
	out := Apiv1{
		internal: &apiInt{
			funcs: lock.NewGenRwLock(make(map[string]*endpoint), "apiv1.funcs"),
			cats:  lock.NewGenRwLock(make(map[string][]string), "apiv1.cats"),
		},
	}

	//	Handle everything in the 404 handler, because this allows us to *remove* endpoints.
	r.NotFoundHandler = http.HandlerFunc(func(res http.ResponseWriter, r *http.Request) {
		if strings.Index(r.URL.Path, _api_v1_) != 0 {
			send404(res, r.URL.Path)
			return
		}
		path := strings.Replace(r.URL.Path, _api_v1_, "", 1)
		isHelp := false
		if strings.Index(path, "help/") == 0 {
			isHelp = true
			path = strings.Replace(path, "help/", "", 1)
		}
		var ep *endpoint
		out.internal.funcs.R().In(func(funcs *map[string]*endpoint) er.R {
			if e, ok := (*funcs)[path]; ok {
				ep = e
			}
			return nil
		})
		var err er.R
		if ep == nil {
			err = send404(res, r.URL.Path)
		} else if isHelp {
			err = ep.respondHelp(res, r)
		} else {
			err = ep.serveHTTP(res, r)
		}
		if err != nil {
			log.Warnf("Error responding to RPC req: [%s]", err)
		}
	})

	//	add a handler for websocket endpoint
	r.Handle(_api_v1_+"websocket", http.HandlerFunc(func(httpResponse http.ResponseWriter, httpRequest *http.Request) {
		webSocketHandler(&out, httpResponse, httpRequest)
	}))

	Endpoint(
		&out,
		"websocket",
		`
		Special endpoint for initiating a websocket connection
		
		This allows further endpoint requests, including streaming endpoints, over the websocket.
		`,
		func(_ *rpc_pb.Null) (*rpc_pb.Null, er.R) {
			return nil, nil
		},
	)

	// Finally, register the master help endpoint
	Endpoint(
		&out,
		"help",
		`
		Output an index of RPC functions which can be called
		
		This is an internal endpoint which provides a manifest of all registered API endpoints in pld.
		`,
		func(_ *rpc_pb.Null) (*help_pb.Category, er.R) {
			return out.masterHelp()
		},
	)

	return &out, r
}
