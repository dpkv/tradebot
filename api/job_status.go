// Copyright (c) 2026 Deepak Vankadaru

package api

const JobStatusPath = "/trader/job/status"

type JobStatusRequest struct{}

type JobStatusItem struct {
	UID        string
	Name       string
	Status     string
	Type       string
	ProductID  string
	ManualFlag bool
	HasStatus  bool

	// Financial data — populated when HasStatus is true.
	Budget       string
	Return       string
	AnnualReturn string
	Days         string
	Buys         int
	Sells        int
	Profit       string
	Fees         string
	BoughtValue  string
	SoldValue    string
	UnsoldValue  string
	SoldSize     string
	UnsoldSize   string
}

type JobStatusResponse struct {
	Jobs []*JobStatusItem
}
