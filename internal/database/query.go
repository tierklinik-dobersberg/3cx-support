package database

import (
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

type QueryOption func(*query)

type query struct {
	from      *time.Time
	to        *time.Time
	agent     *string
	caller    *string
	direction *string
}

func WithFrom(t time.Time) QueryOption {
	return func(q *query) {
		q.from = &t
	}
}

func WithTo(t time.Time) QueryOption {
	return func(q *query) {
		q.to = &t
	}
}

func WithAgent(t string) QueryOption {
	return func(q *query) {
		q.agent = &t
	}
}

func WithCaller(t string) QueryOption {
	return func(q *query) {
		q.caller = &t
	}
}

func WithInbound() QueryOption {
	return func(q *query) {
		t := "Inbound"
		q.direction = &t
	}
}

func WithOutbound() QueryOption {
	return func(q *query) {
		t := "Outbound"
		q.direction = &t
	}
}

func (q *query) build() bson.M {
	result := bson.M{}

	if q.agent != nil {
		result["agent"] = *q.agent
	}

	if q.caller != nil {
		result["caller"] = *q.caller
	}

	if q.direction != nil {
		values := []string{
			"Notanswered",
			"Outbound",
		}

		if *q.direction == "Inbound" {
			values = []string{
				"Inbound",
				"Missed",
				"",
			}
		}

		result["$or"] = bson.D{
			{
				Key:   "direction",
				Value: *q.direction,
			},
			{
				Key: "callType",
				Value: bson.M{
					"$in": values,
				},
			},
		}
	}

	date := bson.M{}

	if q.from != nil {
		date["$gte"] = *q.from
	}

	if q.to != nil {
		date["$lte"] = *q.to
	}

	if len(date) > 0 {
		result["date"] = date
	}

	return result
}
