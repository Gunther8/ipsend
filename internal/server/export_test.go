package server

import "time"

// CheckStalledNow 供测试直接触发停滞检查。
func (s *Server) CheckStalledNow(after time.Duration) { s.checkStalled(after) }
