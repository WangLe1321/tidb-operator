// Copyright 2018 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package util

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pingcap/advanced-statefulset/client/apis/apps/v1/helper"
	"github.com/pingcap/kvproto/pkg/metapb"
	"github.com/pingcap/tidb-operator/pkg/apis/label"
	"github.com/pingcap/tidb-operator/pkg/apis/pingcap/v1alpha1"
	"github.com/pingcap/tidb-operator/pkg/features"
	"github.com/sethvargo/go-password/password"
	apps "k8s.io/api/apps/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/sets"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"
)

var (
	ClusterClientTLSPath   = "/var/lib/cluster-client-tls"
	ClusterAssetsTLSPath   = "/var/lib/cluster-assets-tls"
	TiDBClientTLSPath      = "/var/lib/tidb-client-tls"
	BRBinPath              = "/var/lib/br-bin"
	KVCTLBinPath           = "/var/lib/kvctl-bin"
	DumplingBinPath        = "/var/lib/dumpling-bin"
	LightningBinPath       = "/var/lib/lightning-bin"
	ClusterClientVolName   = "cluster-client-tls"
	DMClusterClientVolName = "dm-cluster-client-tls"
)

const (
	// LastAppliedConfigAnnotation is annotation key of last applied configuration
	LastAppliedConfigAnnotation = "pingcap.com/last-applied-configuration"
)

func GetOrdinalFromPodName(podName string) (int32, error) {
	ordinalStr := podName[strings.LastIndex(podName, "-")+1:]
	ordinalInt, err := strconv.ParseInt(ordinalStr, 10, 32)
	if err != nil {
		return int32(0), err
	}
	return int32(ordinalInt), nil
}

func IsPodOrdinalNotExceedReplicas(pod *corev1.Pod, sts *appsv1.StatefulSet) (bool, error) {
	ordinal, err := GetOrdinalFromPodName(pod.Name)
	if err != nil {
		return false, err
	}
	if features.DefaultFeatureGate.Enabled(features.AdvancedStatefulSet) {
		return helper.GetPodOrdinals(*sts.Spec.Replicas, sts).Has(ordinal), nil
	}
	return ordinal < *sts.Spec.Replicas, nil
}

func getDeleteSlots(tc *v1alpha1.TidbCluster, annKey string) (deleteSlots sets.Int32) {
	deleteSlots = sets.NewInt32()
	annotations := tc.GetAnnotations()
	if annotations == nil {
		return
	}
	value, ok := annotations[annKey]
	if !ok {
		return
	}
	var slice []int32
	err := json.Unmarshal([]byte(value), &slice)
	if err != nil {
		return
	}
	deleteSlots.Insert(slice...)
	return
}

// GetPodOrdinals gets desired ordials of member in given TidbCluster.
func GetPodOrdinals(tc *v1alpha1.TidbCluster, memberType v1alpha1.MemberType) (sets.Int32, error) {
	var ann string
	var replicas int32
	if memberType == v1alpha1.PDMemberType {
		ann = label.AnnPDDeleteSlots
		replicas = tc.Spec.PD.Replicas
	} else if memberType == v1alpha1.PDMSTSOMemberType {
		for _, component := range tc.Spec.PDMS {
			if strings.Contains(memberType.String(), component.Name) {
				replicas = component.Replicas
				ann = label.AnnTSODeleteSlots
				break
			}
		}
	} else if memberType == v1alpha1.PDMSSchedulingMemberType {
		for _, component := range tc.Spec.PDMS {
			if strings.Contains(memberType.String(), component.Name) {
				replicas = component.Replicas
				ann = label.AnnSchedulingDeleteSlots
				break
			}
		}
	} else if memberType == v1alpha1.TiKVMemberType {
		ann = label.AnnTiKVDeleteSlots
		replicas = tc.Spec.TiKV.Replicas
	} else if memberType == v1alpha1.TiDBMemberType {
		ann = label.AnnTiDBDeleteSlots
		replicas = tc.Spec.TiDB.Replicas
	} else if memberType == v1alpha1.TiFlashMemberType {
		ann = label.AnnTiFlashDeleteSlots
		replicas = tc.Spec.TiFlash.Replicas
	} else if memberType == v1alpha1.TiCDCMemberType {
		ann = label.AnnTiCDCDeleteSlots
		replicas = tc.Spec.TiCDC.Replicas
	} else if memberType == v1alpha1.TiProxyMemberType {
		ann = label.AnnTiProxyDeleteSlots
		replicas = tc.Spec.TiProxy.Replicas
	} else {
		return nil, fmt.Errorf("unknown member type %v", memberType)
	}
	deleteSlots := getDeleteSlots(tc, ann)
	maxReplicaCount, deleteSlots := helper.GetMaxReplicaCountAndDeleteSlots(replicas, deleteSlots)
	podOrdinals := sets.NewInt32()
	for i := int32(0); i < maxReplicaCount; i++ {
		if !deleteSlots.Has(i) {
			podOrdinals.Insert(i)
		}
	}
	return podOrdinals, nil
}

func GetDeleteSlotsNumber(annotations map[string]string) (int32, error) {
	value, ok := annotations[helper.DeleteSlotsAnn]
	if !ok {
		return 0, nil
	}
	var slice []int32
	if err := json.Unmarshal([]byte(value), &slice); err != nil {
		return 0, err
	}
	return int32(len(slice)), nil
}

func OrdinalPVCName(memberType v1alpha1.MemberType, setName string, ordinal int32) string {
	return fmt.Sprintf("%s-%s-%d", memberType, setName, ordinal)
}

// IsSubMapOf returns whether the first map is a sub map of the second map
func IsSubMapOf(first map[string]string, second map[string]string) bool {
	for k, v := range first {
		if second == nil {
			return false
		}
		if second[k] != v {
			return false
		}
	}
	return true
}

func GetPodName(tc *v1alpha1.TidbCluster, memberType v1alpha1.MemberType, ordinal int32) string {
	return fmt.Sprintf("%s-%s-%d", tc.Name, memberType.String(), ordinal)
}

func IsStatefulSetUpgrading(set *appsv1.StatefulSet) bool {
	return !(set.Status.CurrentRevision == set.Status.UpdateRevision)
}

func IsStatefulSetScaling(set *appsv1.StatefulSet) bool {
	return !(set.Status.Replicas == *set.Spec.Replicas)
}

func GetStatefulSetName(tc *v1alpha1.TidbCluster, memberType v1alpha1.MemberType) string {
	return fmt.Sprintf("%s-%s", tc.Name, memberType.String())
}

func Encode(obj interface{}) (string, error) {
	b, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func DMClientTLSSecretName(dcName string) string {
	return fmt.Sprintf("%s-dm-client-secret", dcName)
}

func ClusterClientTLSSecretName(tcName string) string {
	return fmt.Sprintf("%s-cluster-client-secret", tcName)
}

func ClusterTLSSecretName(tcName, component string) string {
	return fmt.Sprintf("%s-%s-cluster-secret", tcName, component)
}

func TiDBClientTLSSecretName(tcName string, secretName *string) string {
	if secretName != nil {
		return *secretName
	}
	return fmt.Sprintf("%s-tidb-client-secret", tcName)
}

func TiDBServerTLSSecretName(tcName string) string {
	return fmt.Sprintf("%s-tidb-server-secret", tcName)
}

func TiDBAuthTokenJWKSSecretName(tcName string) string {
	return fmt.Sprintf("%s-tidb-auth-token-jwks-secret", tcName)
}

// SortEnvByName implements sort.Interface to sort env list by name.
type SortEnvByName []corev1.EnvVar

func (e SortEnvByName) Len() int {
	return len(e)
}
func (e SortEnvByName) Swap(i, j int) {
	e[i], e[j] = e[j], e[i]
}

func (e SortEnvByName) Less(i, j int) bool {
	return e[i].Name < e[j].Name
}

// AppendEnv appends envs `b` into `a` ignoring envs whose names already exist
// in `b`.
// Note that this will not change relative order of envs.
func AppendEnv(a []corev1.EnvVar, b []corev1.EnvVar) []corev1.EnvVar {
	aMap := make(map[string]corev1.EnvVar)
	for _, e := range a {
		aMap[e.Name] = e
	}
	for _, e := range b {
		if _, ok := aMap[e.Name]; !ok {
			a = append(a, e)
		}
	}
	return a
}

// AppendOverwriteEnv appends envs b into a and overwrites the envs whose names already exist
// in b.
// Note that this will not change relative order of envs.
func AppendOverwriteEnv(a []corev1.EnvVar, b []corev1.EnvVar) []corev1.EnvVar {
	for _, valNew := range b {
		matched := false
		for j, valOld := range a {
			// It's possible there are multiple instances of the same variable in this array,
			// so we just overwrite all of them rather than trying to resolve dupes here.
			if valNew.Name == valOld.Name {
				a[j] = valNew
				matched = true
			}
		}
		if !matched {
			a = append(a, valNew)
		}
	}
	return a
}

// IsOwnedByTidbCluster checks if the given object is owned by TidbCluster.
// Schema Kind and Group are checked, Version is ignored.
func IsOwnedByTidbCluster(obj metav1.Object) (bool, *metav1.OwnerReference) {
	ref := metav1.GetControllerOf(obj)
	if ref == nil {
		return false, nil
	}
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil {
		return false, nil
	}
	return ref.Kind == v1alpha1.TiDBClusterKind && gv.Group == v1alpha1.SchemeGroupVersion.Group, ref
}

// RetainManagedFields retains the fields in the old object that are managed by kube-controller-manager, such as node ports
func RetainManagedFields(desiredSvc, existedSvc *corev1.Service) {
	// Retain healthCheckNodePort if it has been filled by controller
	desiredSvc.Spec.HealthCheckNodePort = existedSvc.Spec.HealthCheckNodePort
	if desiredSvc.Spec.Type != corev1.ServiceTypeNodePort && desiredSvc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		return
	}
	// Retain NodePorts
	for id, dport := range desiredSvc.Spec.Ports {
		if dport.NodePort != 0 {
			continue
		}
		for _, eport := range existedSvc.Spec.Ports {
			if dport.Port == eport.Port && dport.Protocol == eport.Protocol {
				dport.NodePort = eport.NodePort
				desiredSvc.Spec.Ports[id] = dport
				break
			}
		}
	}
}

// AppendEnvIfPresent appends the given environment if present
func AppendEnvIfPresent(envs []corev1.EnvVar, name string) []corev1.EnvVar {
	for _, e := range envs {
		if e.Name == name {
			return envs
		}
	}
	if val, ok := os.LookupEnv(name); ok {
		envs = append(envs, corev1.EnvVar{
			Name:  name,
			Value: val,
		})
	}
	return envs
}

// MustNewRequirement calls NewRequirement and panics on failure.
func MustNewRequirement(key string, op selection.Operator, vals []string) *labels.Requirement {
	r, err := labels.NewRequirement(key, op, vals)
	if err != nil {
		panic(err)
	}
	return r
}

// BuildStorageVolumeAndVolumeMount builds VolumeMounts and PVCs for volumes declaired in spec.storageVolumes of ComponentSpec
func BuildStorageVolumeAndVolumeMount(storageVolumes []v1alpha1.StorageVolume, defaultStorageClassName *string, memberType v1alpha1.MemberType) ([]corev1.VolumeMount, []corev1.PersistentVolumeClaim) {
	var volMounts []corev1.VolumeMount
	var volumeClaims []corev1.PersistentVolumeClaim
	if len(storageVolumes) > 0 {
		for _, storageVolume := range storageVolumes {
			var tmpStorageClass *string
			quantity, err := resource.ParseQuantity(storageVolume.StorageSize)
			if err != nil {
				klog.Errorf("Cannot parse storage size %v in StorageVolumes of %v, storageVolume Name %s, error: %v", storageVolume.StorageSize, memberType, storageVolume.Name, err)
				continue
			}
			storageRequest := corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: quantity,
				},
			}
			if storageVolume.StorageClassName != nil && len(*storageVolume.StorageClassName) > 0 {
				tmpStorageClass = storageVolume.StorageClassName
			} else {
				tmpStorageClass = defaultStorageClassName
			}
			pvcNameInVCT := string(v1alpha1.GetStorageVolumeName(storageVolume.Name, memberType))
			volumeClaims = append(volumeClaims, VolumeClaimTemplate(storageRequest, pvcNameInVCT, tmpStorageClass))
			if storageVolume.MountPath != "" {
				volMounts = append(volMounts, corev1.VolumeMount{
					Name:      pvcNameInVCT,
					MountPath: storageVolume.MountPath,
				})
			}
		}
	}
	return volMounts, volumeClaims
}

func VolumeClaimTemplate(r corev1.ResourceRequirements, metaName string, storageClassName *string) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: metaName},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			StorageClassName: storageClassName,
			Resources:        r,
		},
	}
}

func MatchLabelFromStoreLabels(storeLabels []*metapb.StoreLabel, componentLabel string) bool {
	storeKind := label.TiKVLabelVal
	for _, storeLabel := range storeLabels {
		if storeLabel.Key == "engine" && storeLabel.Value == label.TiFlashLabelVal {
			storeKind = label.TiFlashLabelVal
			break
		}
	}
	return storeKind == componentLabel
}

// StatefulSetEqual compares the new Statefulset's spec with old Statefulset's last applied config
func StatefulSetEqual(new apps.StatefulSet, old apps.StatefulSet) (equal bool, podTemplateCheckedAndNotEqual bool) {
	// The annotations in old sts may include LastAppliedConfigAnnotation
	tmpAnno := map[string]string{}
	for k, v := range old.Annotations {
		if k != LastAppliedConfigAnnotation && k != label.AnnStsLastSyncTimestamp {
			tmpAnno[k] = v
		}
	}
	if !apiequality.Semantic.DeepEqual(new.Annotations, tmpAnno) {
		return false, false // pod tempate not checked, return false
	}
	oldConfig := apps.StatefulSetSpec{}
	if lastAppliedConfig, ok := old.Annotations[LastAppliedConfigAnnotation]; ok {
		err := json.Unmarshal([]byte(lastAppliedConfig), &oldConfig)
		if err != nil {
			klog.Errorf("unmarshal Statefulset: [%s/%s]'s applied config failed,error: %v", old.GetNamespace(), old.GetName(), err)
			return false, false
		}
		// oldConfig.Template.Annotations may include LastAppliedConfigAnnotation to keep backward compatiability
		// Please check detail in https://github.com/pingcap/tidb-operator/pull/1489
		tmpTemplate := oldConfig.Template.DeepCopy()
		delete(tmpTemplate.Annotations, LastAppliedConfigAnnotation)
		podTemplateEqual := apiequality.Semantic.DeepEqual(*tmpTemplate, new.Spec.Template)
		return apiequality.Semantic.DeepEqual(oldConfig.Replicas, new.Spec.Replicas) &&
			apiequality.Semantic.DeepEqual(oldConfig.UpdateStrategy, new.Spec.UpdateStrategy) && // this will be changed when scaling
			podTemplateEqual, !podTemplateEqual // pod tempate checked, may not equal
	}
	return false, false // pod tempate not checked/exist, return false
}

// ResolvePVCFromPod parses pod volumes definition, and returns all PVCs mounted by this pod
//
// If the Pod don't have any PVC, return error 'NotFound'.
func ResolvePVCFromPod(pod *corev1.Pod, pvcLister corelisterv1.PersistentVolumeClaimLister) ([]*corev1.PersistentVolumeClaim, error) {
	var pvcs []*corev1.PersistentVolumeClaim
	var pvcName string
	for _, vol := range pod.Spec.Volumes {
		if vol.PersistentVolumeClaim != nil {
			pvcName = vol.PersistentVolumeClaim.ClaimName
			if len(pvcName) == 0 {
				continue
			}
			pvc, err := pvcLister.PersistentVolumeClaims(pod.Namespace).Get(pvcName)
			if err != nil {
				klog.Errorf("Get PVC %s/%s error: %v", pod.Namespace, pvcName, err)
				return nil, err
			}
			pvcs = append(pvcs, pvc)
		}
	}
	if len(pvcs) == 0 {
		err := errors.NewNotFound(corev1.Resource("pvc"), "")
		err.ErrStatus.Message = fmt.Sprintf("no pvc found for pod %s/%s", pod.Namespace, pod.Name)
		return pvcs, err
	}
	return pvcs, nil
}

// FixedLengthRandomPasswordBytes generates a random password
func FixedLengthRandomPasswordBytes() []byte {
	return RandomBytes(13)
}

// RandomBytes generates some random bytes that can be used as a token or as a key
func RandomBytes(length int) []byte {
	return []byte(password.MustGenerate(
		length,
		length/3, // number of digits to include in the result
		length/4, // number of symbols to include in the result
		false,    // noUpper
		false,    // allowRepeat
	))
}

// OpenDB opens db
func OpenDB(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open datasource failed, err: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot connect to tidb cluster, err: %v", err)
	}
	return db, nil
}

// SetPassword set tidb password
func SetPassword(ctx context.Context, db *sql.DB, password string) error {

	sql := fmt.Sprintf("SET PASSWORD FOR 'root'@'%%' = '%s'; FLUSH PRIVILEGES;", password)
	_, err := db.ExecContext(ctx, sql)
	return err
}

// GetDSN get tidb dsn
func GetDSN(tc *v1alpha1.TidbCluster, password string) string {
	port := tc.Spec.TiDB.GetServicePort()
	return fmt.Sprintf("root:%s@tcp(%s-tidb.%s.svc:%d)/?charset=utf8mb4,utf8&multiStatements=true",
		password, tc.Name, tc.Namespace, port)
}
