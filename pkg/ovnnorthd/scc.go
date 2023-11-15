package ovnnorthd

import corev1 "k8s.io/api/core/v1"

func getOVNNorthdSecurityContext() *corev1.PodSecurityContext {
	trueVal := true
	runAsUser := int64(OVSUid)
	runAsGroup := int64(OVSGid)

	return &corev1.PodSecurityContext{
		RunAsUser:    &runAsUser,
		RunAsGroup:   &runAsGroup,
		RunAsNonRoot: &trueVal,
		FSGroup:      &runAsGroup,
	}
}
