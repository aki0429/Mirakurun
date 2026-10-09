/*
   Copyright 2016 kanreisa
   Copyright 2026 aki0429 (Go port)

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package server

import (
	"net/http"

	"github.com/aki0429/Mirakurun/go/internal/apid"
)

// handlePrograms は GET /api/programs (src/Mirakurun/api/programs.ts)。
//
// Node 版は query が空なら itemMap.values()、あれば findByQuery(req.query) を返す。
// どちらも挿入順を保つため、Go 版は FindByQuery を常に使う (空 query は全件を挿入順で返す)。
func (s *Server) handlePrograms(w http.ResponseWriter, r *http.Request) {
	q := queryMap(r)

	programs, err := s.app.Program.FindByQuery(q)
	if err != nil {
		if isWhereQueryError(err) {
			s.responseError(w, http.StatusBadRequest)
			return
		}
		s.responseError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if programs == nil {
		programs = []*apid.Program{}
	}
	s.responseJSON(w, programs)
}

// handleProgram は GET /api/programs/{id} (src/Mirakurun/api/programs/{id}.ts)。
func (s *Server) handleProgram(w http.ResponseWriter, r *http.Request) {
	prog := s.app.Program.Get(atoi64(r.PathValue("id")))
	if prog == nil {
		s.responseError(w, http.StatusNotFound)
		return
	}
	s.responseJSON(w, prog)
}
