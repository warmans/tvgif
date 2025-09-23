package web

import (
	"github.com/warmans/tvgif/pkg/mediacache"
	"html/template"
	"net/http"
)

func NewServer(
	addr string,
	overlayDir string,
	overlayCache *mediacache.OverlayCache,
) *Server {
	return &Server{
		addr:         addr,
		overlayDir:   overlayDir,
		overlayCache: overlayCache,
		template: template.Must(template.New("overlays").Parse(`<!doctype html>
<html lang='en'>
	<head>
		<meta charset='utf-8'>
		<title>Overlays</title>
		<style>
			body {
				background-color: #222;
			}
			h1 {
				color: #fff;
			}
			table {
				color: #fff;
				border: none;
			}

			table tr {
				border-bottom: 1px solid #000;
			}
			
			table tr td {
				padding: 10px;
			}
		</style>
	</head>
	<body>
		<header>
			<h1>Available Overlays</h1>
		</header>
		<main>
			<table>
				{{range .}}
					<tr><td><img width="200px" src="/overlays/{{.}}" /></td><td><pre>{{.}}</pre></td></tr>
				{{end}}
			</table>
		</main>
	</body>
</html>`)),
	}
}

type Server struct {
	addr         string
	overlayDir   string
	overlayCache *mediacache.OverlayCache
	template     *template.Template
}

func (s *Server) handleOverlays(resp http.ResponseWriter, req *http.Request) {
	if err := s.template.Execute(resp, s.overlayCache.All()); err != nil {

	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/overlays/index.html", s.handleOverlays)
	mux.Handle("/overlays/", http.StripPrefix("/overlays", http.FileServer(http.Dir(s.overlayDir))))

	return http.ListenAndServe(s.addr, mux)
}
