package translator

import "istio.io/istio/pkg/kube/krt"

// FetchOne resolves a unique input in a reactive compilation or a one-time
// admission snapshot. A nil context performs a read without a subscription.
func FetchOne[T any](ctx krt.HandlerContext, collection krt.Collection[T], options ...krt.FetchOption) *T {
	items := krt.FetchOrList(ctx, collection, options...)
	switch len(items) {
	case 0:
		return nil
	case 1:
		return &items[0]
	default:
		panic("compilation lookup returned multiple objects")
	}
}
