package angelus

import "github.com/jack-work/figaro/api/rpc"

var daemonCapabilities = []rpc.Capability{rpc.CapQuaImages, rpc.CapFormSeq, rpc.CapFormAccess, rpc.CapFormView, rpc.CapNormalize, rpc.CapUnfollow, rpc.CapChannels, rpc.CapStudyKeys, rpc.CapDecideOutfit, rpc.CapFormApply, rpc.CapForkStudies, rpc.CapForkSeat, rpc.CapSeat, rpc.CapPlace, rpc.CapReadUntil}
