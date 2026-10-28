package models

import "time"

type User struct {
	ID         string
	Email      string
	FirstName  string
	LastName   string
	Phone      string
	Street     string
	PostalCode string
	City       string
	Country    string
	CreatedAt  time.Time
}

func (u User) FullName() string {
	if u.LastName == "" {
		return u.FirstName
	}
	return u.FirstName + " " + u.LastName
}

type Store struct {
	ID           int
	Slug         string
	Name         string
	Street       string
	PostalCode   string
	City         string
	Phone        string
	OpeningHours string
}

var AppointmentServices = []struct{ Code, Label, Duration string }{
	{"eye_test", "Free eye test", "20 min"},
	{"contact_lens_fitting", "Contact lens fitting", "30 min"},
	{"frame_adjustment", "Frame adjustment & repair", "10 min"},
	{"kids_eye_test", "Children's eye test", "25 min"},
}

func AppointmentServiceLabel(code string) string {
	for _, s := range AppointmentServices {
		if s.Code == code {
			return s.Label
		}
	}
	return code
}

type Appointment struct {
	ID          string
	StoreName   string
	StoreStreet string
	StoreCity   string
	Service     string
	ScheduledAt time.Time
	Status      string
}

func (a Appointment) ServiceLabel() string { return AppointmentServiceLabel(a.Service) }

func (a Appointment) Upcoming() bool { return a.Status == "booked" && a.ScheduledAt.After(time.Now()) }
