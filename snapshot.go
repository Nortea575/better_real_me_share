package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"strings"
	"time"
)

const maxSnapshotBytes = 2 << 20

type Snapshot struct {
	SchemaVersion   int               `json:"schema_version"`
	Sequence        int64             `json:"sequence"`
	GeneratedAt     string            `json:"generated_at"`
	SourceUpdatedAt *string           `json:"source_updated_at"`
	Actor           SnapshotActor     `json:"actor"`
	Life            SnapshotLife      `json:"life"`
	States          []SnapshotState   `json:"states"`
	Schedule        *SnapshotSchedule `json:"schedule"`
	Location        *SnapshotLocation `json:"location"`
	Sections        SnapshotSections  `json:"sections"`
}

type SnapshotActor struct {
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

type SnapshotLife struct {
	Enabled  bool              `json:"enabled"`
	Paused   bool              `json:"paused"`
	Activity *SnapshotActivity `json:"activity"`
}

type SnapshotActivity struct {
	Name       string `json:"name"`
	StepName   string `json:"step_name"`
	StepIndex  int    `json:"step_index"`
	StepCount  int    `json:"step_count"`
	StartedAt  string `json:"started_at"`
	EndsAt     string `json:"ends_at"`
	TargetPath string `json:"target_path"`
}

type SnapshotState struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Category         string          `json:"category"`
	Type             string          `json:"type"`
	Value            json.RawMessage `json:"value"`
	Min              *float64        `json:"min"`
	Max              *float64        `json:"max"`
	Unit             string          `json:"unit"`
	Stage            string          `json:"stage"`
	StageDescription string          `json:"stage_description"`
	ShowValue        bool            `json:"show_value"`
	Computed         bool            `json:"computed"`
}

type SnapshotSections struct {
	States   bool `json:"states"`
	Schedule bool `json:"schedule"`
	Location bool `json:"location"`
}

type SnapshotSchedule struct {
	Timezone  string                  `json:"timezone"`
	LocalTime string                  `json:"local_time"`
	Active    *SnapshotScheduleEntry  `json:"active"`
	Next      *SnapshotScheduleEntry  `json:"next"`
	Entries   []SnapshotScheduleEntry `json:"entries"`
	Windows   []SnapshotActionWindow  `json:"windows"`
}

type SnapshotScheduleEntry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	StartAt  string `json:"start_at"`
	EndAt    string `json:"end_at"`
}

type SnapshotActionWindow struct {
	ActionID     string `json:"action_id"`
	Name         string `json:"name"`
	Category     string `json:"category"`
	StartAt      string `json:"start_at"`
	EndAt        string `json:"end_at"`
	AvailableNow bool   `json:"available_now"`
}

type SnapshotLocation struct {
	Path        string                  `json:"path"`
	RoomID      string                  `json:"room_id"`
	GroupID     string                  `json:"group_id"`
	RootGroupID string                  `json:"root_group_id"`
	Groups      []SnapshotLocationGroup `json:"groups"`
	Rooms       []SnapshotLocationRoom  `json:"rooms"`
	Connections []SnapshotLocationLink  `json:"connections"`
	Exits       []SnapshotLocationExit  `json:"exits"`
}

type SnapshotLocationGroup struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ParentID    string `json:"parent_id"`
	EntryRoomID string `json:"entry_room_id"`
}

type SnapshotLocationRoom struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	GroupID string `json:"group_id"`
}

type SnapshotLocationLink struct {
	A string `json:"a"`
	B string `json:"b"`
}

type SnapshotLocationExit struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

func decodeSnapshot(body []byte) (Snapshot, []byte, error) {
	var snapshot Snapshot
	if len(body) == 0 || len(body) > maxSnapshotBytes {
		return snapshot, nil, fmt.Errorf("快照大小必须在 1 字节到 2 MiB 之间")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&snapshot); err != nil {
		return snapshot, nil, fmt.Errorf("快照字段无效")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return snapshot, nil, fmt.Errorf("请求必须只包含一个 JSON 对象")
	}
	if err := validateSnapshot(snapshot); err != nil {
		return snapshot, nil, err
	}
	canonical, err := json.Marshal(snapshot)
	if err != nil {
		return snapshot, nil, fmt.Errorf("快照无法序列化")
	}
	return snapshot, canonical, nil
}

func validateSnapshot(s Snapshot) error {
	if s.SchemaVersion != 1 || s.Sequence < 1 {
		return fmt.Errorf("快照版本或序号无效")
	}
	if !validTimestamp(s.GeneratedAt) {
		return fmt.Errorf("generated_at 必须是 RFC3339 时间")
	}
	if s.SourceUpdatedAt != nil && !validTimestamp(*s.SourceUpdatedAt) {
		return fmt.Errorf("source_updated_at 必须是 RFC3339 时间或 null")
	}
	if !validText(s.Actor.Name, 1, 120) || !validText(s.Actor.AvatarURL, 0, 2048) {
		return fmt.Errorf("角色展示信息无效")
	}
	if s.Actor.AvatarURL != "" {
		u, err := url.Parse(s.Actor.AvatarURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return fmt.Errorf("头像地址必须是 HTTPS URL")
		}
	}
	if s.States == nil || len(s.States) > 200 || !s.Sections.States && len(s.States) != 0 {
		return fmt.Errorf("状态列表与共享选项不一致")
	}
	for _, state := range s.States {
		if !validText(state.ID, 1, 128) || !validText(state.Name, 1, 160) || !validText(state.Category, 0, 120) || !validText(state.Type, 1, 40) || !validText(state.Unit, 0, 64) || !validText(state.Stage, 0, 160) || !validText(state.StageDescription, 0, 512) {
			return fmt.Errorf("状态字段无效")
		}
		if err := validateScalar(state.Value); err != nil {
			return fmt.Errorf("状态值必须是标量")
		}
		if state.Min != nil && (math.IsNaN(*state.Min) || math.IsInf(*state.Min, 0)) || state.Max != nil && (math.IsNaN(*state.Max) || math.IsInf(*state.Max, 0)) {
			return fmt.Errorf("状态范围无效")
		}
	}
	if !s.Sections.Schedule && s.Schedule != nil || s.Sections.Schedule && s.Schedule == nil {
		return fmt.Errorf("日程内容与共享选项不一致")
	}
	if !s.Sections.Location && s.Location != nil || s.Sections.Location && s.Location == nil {
		return fmt.Errorf("位置内容与共享选项不一致")
	}
	if !s.Sections.Schedule && s.Life.Activity != nil {
		return fmt.Errorf("未共享日程时不能包含活动")
	}
	if s.Life.Activity != nil {
		a := s.Life.Activity
		if !validText(a.Name, 1, 160) || !validText(a.StepName, 0, 160) || !validText(a.TargetPath, 0, 512) || a.StepIndex < 0 || a.StepCount < 0 || a.StepCount > 10000 || a.StepIndex > a.StepCount || !validTimestamp(a.StartedAt) || !validTimestamp(a.EndsAt) {
			return fmt.Errorf("活动字段无效")
		}
		if !s.Sections.Location && a.TargetPath != "" {
			return fmt.Errorf("未共享位置时活动路径必须为空")
		}
	}
	if s.Schedule != nil {
		if !validText(s.Schedule.Timezone, 1, 80) || !validTimestamp(s.Schedule.LocalTime) || len(s.Schedule.Entries) > 500 || len(s.Schedule.Windows) > 500 {
			return fmt.Errorf("日程字段无效")
		}
		if _, err := time.LoadLocation(s.Schedule.Timezone); err != nil {
			return fmt.Errorf("日程时区无效")
		}
		for _, entry := range s.Schedule.Entries {
			if err := validateScheduleEntry(entry); err != nil {
				return err
			}
		}
		if s.Schedule.Active != nil {
			if err := validateScheduleEntry(*s.Schedule.Active); err != nil {
				return err
			}
		}
		if s.Schedule.Next != nil {
			if err := validateScheduleEntry(*s.Schedule.Next); err != nil {
				return err
			}
		}
		for _, window := range s.Schedule.Windows {
			if !validText(window.ActionID, 1, 128) || !validText(window.Name, 1, 160) || !validText(window.Category, 0, 120) || !validTimestamp(window.StartAt) || !validTimestamp(window.EndAt) {
				return fmt.Errorf("日程窗口字段无效")
			}
		}
	}
	if s.Location != nil {
		l := s.Location
		if !validText(l.Path, 0, 512) || !validText(l.RoomID, 0, 128) || !validText(l.GroupID, 0, 128) || !validText(l.RootGroupID, 0, 128) || len(l.Groups) > 500 || len(l.Rooms) > 1000 || len(l.Connections) > 2000 || len(l.Exits) > 500 {
			return fmt.Errorf("位置字段无效")
		}
		for _, group := range l.Groups {
			if !validText(group.ID, 1, 128) || !validText(group.Name, 1, 160) || !validText(group.ParentID, 0, 128) || !validText(group.EntryRoomID, 0, 128) {
				return fmt.Errorf("位置分组字段无效")
			}
		}
		for _, room := range l.Rooms {
			if !validText(room.ID, 1, 128) || !validText(room.Name, 1, 160) || !validText(room.GroupID, 0, 128) {
				return fmt.Errorf("位置房间字段无效")
			}
		}
		for _, link := range l.Connections {
			if !validText(link.A, 1, 128) || !validText(link.B, 1, 128) {
				return fmt.Errorf("位置连接字段无效")
			}
		}
		for _, exit := range l.Exits {
			if !validText(exit.ID, 1, 128) || !validText(exit.Name, 1, 160) || !validText(exit.Path, 0, 512) {
				return fmt.Errorf("位置出口字段无效")
			}
		}
	}
	return nil
}

func validateScheduleEntry(entry SnapshotScheduleEntry) error {
	if !validText(entry.ID, 1, 128) || !validText(entry.Name, 1, 160) || !validText(entry.Category, 0, 120) || !validTimestamp(entry.StartAt) || !validTimestamp(entry.EndAt) {
		return fmt.Errorf("日程条目字段无效")
	}
	return nil
}

func validateScalar(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("missing")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return err
	}
	switch v := value.(type) {
	case nil, bool, string:
		if str, ok := v.(string); ok && len(str) > 4096 {
			return fmt.Errorf("string too long")
		}
	case json.Number:
		if n, err := v.Float64(); err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return fmt.Errorf("number invalid")
		}
	default:
		return fmt.Errorf("not scalar")
	}
	return nil
}

func validTimestamp(value string) bool {
	if value == "" {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func validText(value string, min, max int) bool {
	return len(value) >= min && len(value) <= max && !strings.ContainsAny(value, "\x00\r\n")
}
