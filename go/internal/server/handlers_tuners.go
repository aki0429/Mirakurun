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

const busyJobReason = "job is missing or already been abort requested or other unacceptable state"

// handleTuners は GET /api/tuners (src/Mirakurun/api/tuners.ts)。
func (s *Server) handleTuners(w http.ResponseWriter, r *http.Request) {
	s.responseJSON(w, s.tuner.Devices())
}

// handleTuner は GET /api/tuners/{index} (src/Mirakurun/api/tuners/{index}.ts)。
func (s *Server) handleTuner(w http.ResponseWriter, r *http.Request) {
	dev := s.tuner.Get(atoi(r.PathValue("index")))
	if dev == nil {
		s.responseError(w, http.StatusNotFound)
		return
	}
	s.responseJSON(w, dev.Status())
}

// handleTunerProcess は GET /api/tuners/{index}/process (src/Mirakurun/api/tuners/{index}/process.ts)。
func (s *Server) handleTunerProcess(w http.ResponseWriter, r *http.Request) {
	dev := s.tuner.Get(atoi(r.PathValue("index")))
	if dev == nil {
		s.responseError(w, http.StatusNotFound)
		return
	}
	st := dev.Status()
	if st.PID == nil {
		s.responseError(w, http.StatusNotFound)
		return
	}
	s.responseJSON(w, apid.TunerProcess{PID: *st.PID})
}

// handleKillTunerProcess は DELETE /api/tuners/{index}/process。
func (s *Server) handleKillTunerProcess(w http.ResponseWriter, r *http.Request) {
	dev := s.tuner.Get(atoi(r.PathValue("index")))
	if dev == nil {
		s.responseError(w, http.StatusNotFound)
		return
	}
	if dev.Status().PID == nil {
		s.responseError(w, http.StatusNotFound)
		return
	}
	dev.KillProcess()
	w.WriteHeader(http.StatusNoContent)
}

// handleJobs は GET /api/jobs (src/Mirakurun/api/jobs.ts)。
func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	s.responseJSON(w, s.app.Job.Jobs())
}

// handleJobAbort は PUT /api/jobs/{id}/abort。
func (s *Server) handleJobAbort(w http.ResponseWriter, r *http.Request) {
	if s.app.Job.Abort(r.PathValue("id"), "requested by user") {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	s.responseError(w, http.StatusConflict, busyJobReason)
}

// handleJobRerun は PUT /api/jobs/{id}/rerun。
func (s *Server) handleJobRerun(w http.ResponseWriter, r *http.Request) {
	if s.app.Job.Rerun(r.PathValue("id")) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	s.responseError(w, http.StatusConflict, busyJobReason)
}

// handleJobSchedules は GET /api/job-schedules。
func (s *Server) handleJobSchedules(w http.ResponseWriter, r *http.Request) {
	s.responseJSON(w, s.app.Job.Schedules())
}

// handleJobScheduleRun は PUT /api/job-schedules/{key}/run。
//
// Node 版は `schedules.find(...) === null` (Array#find は undefined を返すため決して真にならない)
// という到達不能な 404 分岐を持ち、キーが存在しなくても常に 202 を返す。挙動互換のため同じく
// RunSchedule を呼んで常に 202 を返す (存在しないキーは manager 側が warn ログを出して無視する)。
func (s *Server) handleJobScheduleRun(w http.ResponseWriter, r *http.Request) {
	s.app.Job.RunSchedule(r.PathValue("key"))
	w.WriteHeader(http.StatusAccepted)
}
