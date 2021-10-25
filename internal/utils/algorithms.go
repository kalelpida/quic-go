package utils

import ("strings")

type StartAlgo int
const (
	ChooseSlowStart StartAlgo = iota + 1
	ChooseHystart
	ChooseHystartpp
)
type CongestionAlgo int
const (
	ChooseNewReno CongestionAlgo = iota + 1
	ChooseCubic
)

//converts option string to start algo
func String2Start(nomAlgo string) StartAlgo {
	nom := strings.ToLower(nomAlgo)
	switch nom{
	case "slowstart", "ss":
		return ChooseSlowStart
	case "hystart", "h":
		return ChooseHystart
	case "hystartpp", "hystart++", "hpp", "h++":
		return ChooseHystartpp
	default:
		return ChooseHystart
	}
}

//convert option string to congestion algo
func String2Congestion(nomAlgo string) CongestionAlgo {
	nom := strings.ToLower(nomAlgo)
	switch nom{
	case "cubic", "c":
		return ChooseCubic
	case "newreno", "reno", "nr":
		return ChooseNewReno
	default:
		return ChooseNewReno
	}
}