package pkg

import (
	"context"
	"fmt"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	podinformer "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corelist "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"log"
	"reflect"
	"time"
)

const (
	maxprocess = 2
	maxretry   = 2
	count      = 1
)

type Controller struct {
	client  *kubernetes.Clientset
	podList corelist.PodLister
	queue   workqueue.RateLimitingInterface
}

func (c *Controller) add(obj interface{}) {
	c.enqueue(obj)
}

func (c *Controller) update(oldObj interface{}, newObj interface{}) {
	//比较annotation,深度比较,相同的话直接返回
	if reflect.DeepEqual(oldObj, newObj) {
		return
	}
	//如果有更改，则将新对象加入到处理队列中,否则结束执行。(存ns-pod key名)
	c.enqueue(newObj)
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		return
	}
	c.queue.Add(key)
}

func (c *Controller) podsync(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		log.Println(err)
		return err
	}

	getpod, err := c.podList.Pods(namespace).Get(name)

	//if errors.IsNotFound(err) {
	//	time.Sleep(6 * time.Second)
	//	getpod, err = c.podList.Pods(namespace).Get(name)
	//}

	if err != nil && errors.IsNotFound(err) {
		return err
	}
	getnode, err := c.client.CoreV1().Nodes().Get(context.TODO(), getpod.Spec.NodeName, v1.GetOptions{})
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		log.Println(err)
		return err
	}
	_, ok := getpod.GetAnnotations()["watch-podrestart"]

	//拿rc名称,查rc的owner，拿deploy的名称。
	controllerkind := getpod.OwnerReferences[0].Kind
	if controllerkind != "ReplicaSet" {
		return nil
	}
	rsname := getpod.OwnerReferences[0].Name
	rsget, err := c.client.AppsV1().ReplicaSets(namespace).Get(context.TODO(), rsname, v1.GetOptions{})

	if err != nil && errors.IsNotFound(err) {
		log.Println(err)
		return err
	}
	deployname := rsget.OwnerReferences[0].Name
	//拿节点状态
	//pod状态为pending时拿不到node状态
	if getpod.Status.Phase == "Pending" {
		return nil
	}

	nodestatus := getnode.Status.Conditions[4].Status
	fmt.Println(nodestatus)
	//建字典
	if ok {
		podcurr1 := getpod.Status.ContainerStatuses[0].RestartCount
		fmt.Println(podcurr1)
		//一分钟执行一次
		ticker := time.NewTicker(1 * time.Minute)
		//执行完关闭
		defer ticker.Stop()
		//5秒钟后执行
		<-time.After(5 * time.Second)
		for {
			select {
			case <-ticker.C:
				podcurr2 := getpod.Status.ContainerStatuses[0].RestartCount
				fmt.Println(podcurr2)
				if podcurr2-podcurr1 >= int32(count) {
					//发送邮件 后续写
					//拿deploy
					deploy, err := c.client.AppsV1().Deployments(namespace).Get(context.TODO(), deployname, v1.GetOptions{})
					if err != nil && errors.IsNotFound(err) {
						log.Println(err)
						return err
					}
					//设置副本为0
					zero := int32(0)
					deploy.Spec.Replicas = &zero
					//更新副本数,更新时注意要用get时的deploy变量
					deploy, err = c.client.AppsV1().Deployments(namespace).Update(context.TODO(), deploy, v1.UpdateOptions{})
					if err != nil {
						log.Println(err)
						return err
					}
					fmt.Printf("deploy %v 副本数已更新为0", deploy.Spec.Replicas)

				}
			}
		}

	} else if ok && nodestatus != "True" {
		return nil
	}
	return nil
}

func (c *Controller) Run(stopCh chan struct{}) {
	for i := 0; i < maxprocess; i++ {
		//启动5个协程，每隔一分钟，就去运行work，直到接收到结束信号 就关闭这个协程，pod事件处理失败后重试10次
		go wait.Until(c.work, time.Minute, stopCh)
	}
	<-stopCh
}

func (c *Controller) work() {
	for c.process() {

	}
}

func (c *Controller) process() bool {
	item, shutdown := c.queue.Get()
	//如果收到停止讯号返回false退出循环
	if shutdown {
		return false
	}
	//标记拿到的项目已完成
	defer c.queue.Done(item)
	key := item.(string)
	err := c.podsync(key)
	if err != nil {
		c.handlererror(key, err)
	}
	return true
}

func (c *Controller) handlererror(key string, err error) {
	//重试10次
	if c.queue.NumRequeues(key) <= maxretry {
		c.queue.AddRateLimited(key)
	}
	runtime.HandleError(err)
	c.queue.Done(key)
}

func Newcontroller(client *kubernetes.Clientset, podInformer podinformer.PodInformer) Controller {
	c := Controller{
		client:  client,
		podList: podInformer.Lister(),
		queue:   workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
	}
	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: c.update,
		AddFunc:    c.add,
	})
	return c
}
