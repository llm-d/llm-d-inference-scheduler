package batch

import "reflect"

func NewRandomRobinPolicy() RequestMergePolicy {
	return &RandomRobinPolicy{}
}

type RandomRobinPolicy struct {
}

func (r *RandomRobinPolicy) MergeRequestChannels(channels []RequestChannel) RequestChannel {
	mergedChannel := make(chan RequestMessage)

	cases := make([]reflect.SelectCase, len(channels))
	for i, ch := range channels {
		cases[i] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ch.Channel)}
	}

	go func() {
		for {
			i1, val, ok := reflect.Select(cases)
			if !ok {
				// one of the channels is closed, remove it
				newCases := make([]reflect.SelectCase, 0, len(cases)-1)
				for i2, c := range cases {
					if i2 != i1 {
						newCases = append(newCases, c)
					}
				}
				cases = newCases
				if len(cases) == 0 {
					close(mergedChannel)
					break
				}
			} else {
				mergedChannel <- val.Interface().(RequestMessage)
			}

		}
	}()

	return RequestChannel{
		Channel:  mergedChannel,
		Metadata: map[string]any{},
	}
}
