package devserver

import (
	"net/http"

	"github.com/smart-minds/keel/internal/config"
	"github.com/smart-minds/keel/internal/web"
)

type manifestReq struct {
	dep        *config.Deployment
	targetName string
	backURL    string
	action     string
	project    string
}

func (s *Server) serveManifestBuilder(w http.ResponseWriter, r *http.Request, req manifestReq) {
	web.ServeManifestBuilder(s.Renderer, w, r, web.ManifestRequest{
		Base:       s.base(w, r, "Variable manifest"),
		Dep:        req.dep,
		DepURL:     depURL(req.dep.Name),
		TargetName: req.targetName,
		Project:    req.project,
		FormAction: req.action,
		BackURL:    req.backURL,
	})
}
