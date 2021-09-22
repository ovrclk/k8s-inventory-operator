# K8S Inventory operator for Akash provider

Monitor K8S resources and report them via REST API

## Resources
- StorageClasses
  - Rook+Ceph

Provider can talk with service endpoint 
```go
// discover inventory operator
svcResult, err := c.kc.CoreV1().Services("").List(ctx, metav1.ListOptions{
    LabelSelector: builder.AkashManagedLabelName + "=true" +
        ",app.kubernetes.io/name=akash" +
        ",app.kubernetes.io/instance=inventory" +
        ",app.kubernetes.io/component=operator",
})
if err != nil {
    return nil, err
}

if len(svcResult.Items) == 0 {
    return nil, nil
}

// request inventory snapshot
result := c.kc.CoreV1().RESTClient().Get().
    Namespace(svcResult.Items[0].Namespace).
    Resource("services").
    Name(svcResult.Items[0].Name + ":api").
    SubResource("proxy").
    Suffix("inventory").
    Do(ctx)

if err := result.Error(); err != nil {
    return nil, err
}

inv := &akashv1.Inventory{}

if err := result.Into(inv); err != nil {
    return nil, err
}

```

## Deploy
```shell
kubectl apply -f example/inventory-operator.yaml
```
