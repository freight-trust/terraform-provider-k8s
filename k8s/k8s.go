package k8s

import (
	"fmt"
	"log"
	"reflect"
	"unsafe"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/dynamic"
	"k8s.io/kube-openapi/pkg/util/proto"

	"github.com/hashicorp/terraform-plugin-sdk/helper/resource"
	tfSchema "github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/terraform"
)

func BuildResourcesMap() map[string]*tfSchema.Resource {
	resourcesMap := map[string]*tfSchema.Resource{}
	resourceVerbs := []string{"create", "get"}

	K8SConfig_Singleton().ForEachAPIResource(func(apiResource metav1.APIResource, gvk schema.GroupVersionKind) {
		if len(apiResource.Verbs) > 0 && !sets.NewString(apiResource.Verbs...).HasAll(resourceVerbs...) {
			return
		}

		model := K8SConfig_Singleton().ModelsMap[gvk]
		if model == nil {
			//log.Println("no model for:", apiResource, gvk)
			//skip resources without a model
			return
		}

		resourceKey := ResourceKey(gvk.Group, gvk.Version, apiResource.Kind)
		//log.Println("gvk:", gvk, "resource:", resourceKey)
		if _, hasKey := resourcesMap[resourceKey]; hasKey {
			//dups
			//sometimes resources appear more than once
			return
		}

		resourceSchema := K8SConfig_Singleton().TFSchemasMap[resourceKey]
		if resourceSchema == nil {
			log.Fatalln("Schema not found for:" + resourceKey)
		}

		resource := tfSchema.Resource{Schema: resourceSchema.Elem.(*tfSchema.Resource).Schema}
		isNamespaced := apiResource.Namespaced

		resource.Exists = func(resourceData *tfSchema.ResourceData, meta interface{}) (bool, error) {
			return resourceExists(&gvk, isNamespaced, model, resourceData, meta)
		}
		resource.Create = func(resourceData *tfSchema.ResourceData, meta interface{}) error {
			return resourceCreate(resourceKey, &gvk, isNamespaced, model, resourceData, meta)
		}
		resource.Read = func(resourceData *tfSchema.ResourceData, meta interface{}) error {
			return resourceRead(resourceKey, &gvk, isNamespaced, model, resourceData, meta)
		}
		resource.Update = func(resourceData *tfSchema.ResourceData, meta interface{}) error {
			return resourceUpdate(resourceKey, &gvk, isNamespaced, model, resourceData, meta)
		}
		resource.Delete = func(resourceData *tfSchema.ResourceData, meta interface{}) error {
			return resourceDelete(&gvk, isNamespaced, model, resourceData, meta)
		}
		resource.Importer = &tfSchema.ResourceImporter{
			State: tfSchema.ImportStatePassthrough,
		}

		resource.CustomizeDiff = customizeDiff
		resourcesMap[resourceKey] = &resource
	})

	return resourcesMap
}

// callback to enable making changes to the proposed plan
func customizeDiff(d *tfSchema.ResourceDiff, meta interface{}) error {
	//https://stackoverflow.com/questions/42664837/access-unexported-fields-in-golang-reflect
	rd := reflect.ValueOf(d).Elem()
	rdiff := rd.FieldByName("diff")
	diff := reflect.NewAt(rdiff.Type(), unsafe.Pointer(rdiff.UnsafeAddr())).Elem().Interface().(*terraform.InstanceDiff)
	rconfig := rd.FieldByName("config")
	config := reflect.NewAt(rconfig.Type(), unsafe.Pointer(rconfig.UnsafeAddr())).Elem().Interface().(*terraform.ResourceConfig)
	id := config.Config["id"]
	create := id == nil

	// config.Raw contains the original config needed for making updates
	k8sConfig := meta.(*K8SConfig)
	if id != nil {
		k8sConfig.StoreResourceConfig(id.(string), config)
	}
	//Dump(config.Raw)

	//HACK AROUND THIS https://github.com/hashicorp/terraform/commit/7b6710540795f20b7d36affd735b9b0dcd3c7520
	// if we're not creating a new resource, remove any new computed fields
	if !create {
		for attr, d := range diff.Attributes {
			// If there's no change, then don't let this go through as NewComputed.
			// This usually only happens when Old and New are both empty.
			if d.NewComputed && d.Old == d.New {
				delete(diff.Attributes, attr)
			}
		}
	}
	return nil
}


func BuildDataSourcesMap() map[string]*tfSchema.Resource {
	resourcesMap := map[string]*tfSchema.Resource{}
	resourceVerbs := []string{"get"}

	K8SConfig_Singleton().ForEachAPIResource(func(apiResource metav1.APIResource, gvk schema.GroupVersionKind) {
		if len(apiResource.Verbs) > 0 && !sets.NewString(apiResource.Verbs...).HasAll(resourceVerbs...) {
			return
		}

		model := K8SConfig_Singleton().ModelsMap[gvk]
		if model == nil {
			//log.Println("no model for:", apiResource, gvk)
			//skip resources without a model
			return
		}

		resourceKey := ResourceKey(gvk.Group, gvk.Version, apiResource.Kind)
		//log.Println("gvk:", gvk, "resource:", resourceKey)
		if _, hasKey := resourcesMap[resourceKey]; hasKey {
			//dups
			//sometimes resources appear more than once
			return
		}

		resourceSchema := K8SConfig_Singleton().TFSchemasMap[resourceKey]
		if resourceSchema == nil {
			log.Fatalln("Schema not found for:" + resourceKey)
		}

		resource := tfSchema.Resource{Schema: resourceSchema.Elem.(*tfSchema.Resource).Schema}
		isNamespaced := apiResource.Namespaced

		resource.Read = func(resourceData *tfSchema.ResourceData, meta interface{}) error {
			return datasourceRead(resourceKey, &gvk, isNamespaced, model, resourceData, meta)
		}

		resourcesMap[resourceKey] = &resource
	})

	return resourcesMap
}

func datasourceRead(resourceKey string, gvk *schema.GroupVersionKind, isNamespaced bool, model proto.Schema, resourceData *tfSchema.ResourceData, meta interface{}) error {
	k8sConfig := meta.(*K8SConfig)
	name, nameErr := getName(resourceData)
	if nameErr != nil {
		return nameErr
	}
	namespace := getNamespace(isNamespaced, resourceData, k8sConfig)
	resourceData.SetId(CreateId(namespace, gvk.Kind, name))

	return resourceRead(resourceKey, gvk, isNamespaced, model, resourceData, meta)
}

func resourceExists(gvk *schema.GroupVersionKind, isNamespaced bool, model proto.Schema, resourceData *tfSchema.ResourceData, meta interface{}) (bool, error) {
	//log.Println("resourceExists Id:", resourceData.Id())
	k8sConfig := meta.(*K8SConfig)
	namespace, _, name, err := ParseId(resourceData.Id())
	if err != nil {
		return false, err
	}

	if _, err := k8sConfig.Get(name, metav1.GetOptions{}, gvk, namespace); err != nil {
		if statusErr, ok := err.(*errors.StatusError); ok && statusErr.ErrStatus.Code == 404 {
			log.Println("not exists:", resourceData.Id(), "err:", err)
			return false, nil
		} else {
			return false, err
		}
	}
	return true, nil
}

func resourceCreate(resourceKey string, gvk *schema.GroupVersionKind, isNamespaced bool, model proto.Schema, resourceData *tfSchema.ResourceData, meta interface{}) error {
	k8sConfig := meta.(*K8SConfig)
	name, nameErr := getName(resourceData)
	if nameErr != nil {
		return nameErr
	}
	namespace := getNamespace(isNamespaced, resourceData, k8sConfig)

	visitor := NewTF2K8SVisitor(resourceData, nil, "", "", nil)
	model.Accept(visitor)
	visitorObject := visitor.Object.(map[string]interface{})
	visitorObject["apiVersion"] = gvk.Group + "/" + gvk.Version
	visitorObject["kind"] = gvk.Kind

	raw := unstructured.Unstructured{
		Object: visitorObject,
	}

	RESTMapping, restMapperErr := k8sConfig.RESTMapper.RESTMapping(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}, gvk.Version)
	if restMapperErr != nil {
		return restMapperErr
	}
	var resourceClient dynamic.ResourceInterface
	resourceClient = k8sConfig.DynamicClient.Resource(RESTMapping.Resource)
	if isNamespaced {
		resourceClient = resourceClient.(dynamic.NamespaceableResourceInterface).Namespace(namespace)
	}
	log.Println("create req:", raw)
	res, err := resourceClient.Create(&raw, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	//log.Println("create res:", res)

	resourceData.SetId(CreateId(namespace, gvk.Kind, name))
	return setState(resourceKey, res.Object, model, resourceData)
}

func resourceRead(resourceKey string, gvk *schema.GroupVersionKind, isNamespaced bool, model proto.Schema, resourceData *tfSchema.ResourceData, meta interface{}) error {
	//log.Println("resourceRead Id:", resourceData.Id())
	k8sConfig := meta.(*K8SConfig)
	namespace, _, name, nameErr := ParseId(resourceData.Id())
	if nameErr != nil {
		return nameErr
	}

	res, err := k8sConfig.Get(name, metav1.GetOptions{}, gvk, namespace)
	if err != nil {
		return err
	}
	//log.Println("read res:", res)

	return setState(resourceKey, res.Object, model, resourceData)
}

func resourceUpdate(resourceKey string, gvk *schema.GroupVersionKind, isNamespaced bool, model proto.Schema, resourceData *tfSchema.ResourceData, meta interface{}) error {
	k8sConfig := meta.(*K8SConfig)
	namespace, _, name, nameErr := ParseId(resourceData.Id())
	if nameErr != nil {
		return nameErr
	}

	resourceConfig, _ := k8sConfig.LoadResourceConfig(resourceData.Id())
	visitor := NewTF2K8SVisitor(resourceData, resourceConfig, "", "", nil)
	model.Accept(visitor)
	log.Println("ops:", visitor.ops)

	jsonBytes, err := PatchOperations(visitor.ops).MarshalJSON()
	if err != nil {
		return err
	}

	RESTMapping, _ := k8sConfig.RESTMapper.RESTMapping(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}, gvk.Version)
	var resourceClient dynamic.ResourceInterface
	resourceClient = k8sConfig.DynamicClient.Resource(RESTMapping.Resource)
	if isNamespaced {
		resourceClient = resourceClient.(dynamic.NamespaceableResourceInterface).Namespace(namespace)
	}
	res, err := resourceClient.Patch(name, types.JSONPatchType, jsonBytes, metav1.PatchOptions{})
	if err != nil {
		return err
	}
	log.Println("update res:", res)
	return setState(resourceKey, res.Object, model, resourceData)
}

func setState(resourceKey string, state map[string]interface{}, model proto.Schema, resourceData *tfSchema.ResourceData) error {
	visitor := NewK8S2TFReadVisitor(resourceKey, state)
	model.Accept(visitor)
	visitorObject := visitor.Object.([]interface{})[0].(map[string]interface{})

	for key, value := range visitorObject {
		if err := resourceData.Set(ToSnake(key), value); err != nil {
			return err
		}
	}

	return nil
}

func resourceDelete(gvk *schema.GroupVersionKind, isNamespaced bool, model proto.Schema, resourceData *tfSchema.ResourceData, meta interface{}) error {
	//log.Println("resourceDelete Id:", resourceData.Id())
	k8sConfig := meta.(*K8SConfig)
	namespace, _, name, nameErr := ParseId(resourceData.Id())
	if nameErr != nil {
		return nameErr
	}

	RESTMapping, _ := k8sConfig.RESTMapper.RESTMapping(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind}, gvk.Version)
	var resourceClient dynamic.ResourceInterface
	resourceClient = k8sConfig.DynamicClient.Resource(RESTMapping.Resource)
	if isNamespaced {
		resourceClient = resourceClient.(dynamic.NamespaceableResourceInterface).Namespace(namespace)
	}
	deletePolicy := metav1.DeletePropagationForeground
	err := resourceClient.Delete(name, &metav1.DeleteOptions{PropagationPolicy: &deletePolicy})
	if err != nil {
		return err
	}

	resourceData.SetId("")
	return resource.Retry(resourceData.Timeout(tfSchema.TimeoutDelete), func() *resource.RetryError {
		res, err := k8sConfig.GetOne(name, gvk, namespace)
		log.Println("Deleting:", res, err)
		if statusErr, ok := err.(*errors.StatusError); ok && statusErr.ErrStatus.Code == 404 {
			return resource.NonRetryableError(nil)
		} else {
			return resource.RetryableError(fmt.Errorf("Waiting for %s to be deleted", name))
		}
	})
}

func getName(resourceData *tfSchema.ResourceData) (string, error) {
	name, ok := resourceData.GetOk("metadata.0.name")
	if ok {
		return name.(string), nil
	} else {
		return "", fmt.Errorf("metadata.0.name not found")
	}
}

func getNamespace(isNamespaced bool, resourceData *tfSchema.ResourceData, k8sConfig *K8SConfig) string {
	if !isNamespaced {
		return ""
	}
	namespace := resourceData.Get("metadata.0.namespace").(string)
	if namespace != "" {
		return namespace
	} else {
		return k8sConfig.Namespace
	}
}
