.PHONY: fmt fmt-check vet test build ci docker-build docker-pull docker-tag docker-push kind-up kind-load deploy deploy-yaml smoke-test e2e rollback kind-down

IMAGE_PREFIX ?= k8s-ops-platform
TAG ?= dev
KIND_CLUSTER ?= k8s-ops
NAMESPACE ?= k8s-ops-platform
HELM_RELEASE ?= k8s-ops-platform
CHART ?= charts/k8s-ops-platform
SOURCE ?= https://github.com/example/k8s-ops-platform

fmt:
	gofmt -w $$(find . -name '*.go' -type f -not -path './vendor/*')

fmt-check:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -type f -not -path './vendor/*'))" || \
		{ echo "gofmt required for:"; gofmt -l $$(find . -name '*.go' -type f -not -path './vendor/*'); exit 1; }

vet:
	go vet ./...

test:
	go test ./...

build:
	go build ./cmd/agent ./cmd/action-controller

ci: fmt-check vet test build

docker-build:
	docker build --build-arg VERSION=$(TAG) --build-arg SOURCE=$(SOURCE) -f Dockerfile.agent -t $(IMAGE_PREFIX)-agent:$(TAG) .
	docker build --build-arg VERSION=$(TAG) --build-arg SOURCE=$(SOURCE) -f Dockerfile.controller -t $(IMAGE_PREFIX)-controller:$(TAG) .

docker-pull:
	docker pull $(IMAGE_PREFIX)-agent:$(TAG)
	docker pull $(IMAGE_PREFIX)-controller:$(TAG)

docker-tag:
	docker tag $(IMAGE_PREFIX)-agent:$(TAG) $(IMAGE_PREFIX)-agent:$(TARGET_TAG)
	docker tag $(IMAGE_PREFIX)-controller:$(TAG) $(IMAGE_PREFIX)-controller:$(TARGET_TAG)

docker-push:
	docker push $(IMAGE_PREFIX)-agent:$(TAG)
	docker push $(IMAGE_PREFIX)-controller:$(TAG)

kind-up:
	./scripts/kind-up.sh $(KIND_CLUSTER)

kind-load:
	kind load docker-image --name $(KIND_CLUSTER) $(IMAGE_PREFIX)-agent:$(TAG) $(IMAGE_PREFIX)-controller:$(TAG)

deploy:
	helm upgrade --install $(HELM_RELEASE) $(CHART) \
		--namespace $(NAMESPACE) --create-namespace \
		--set namespace=$(NAMESPACE) \
		--set agent.image.repository=$(IMAGE_PREFIX)-agent \
		--set controller.image.repository=$(IMAGE_PREFIX)-controller \
		--set image.tag=$(TAG) --wait --timeout 3m

deploy-yaml:
	kubectl apply -f config/manager/namespace.yaml
	kubectl apply -f config/crd
	kubectl apply -f config/rbac
	kubectl apply -f config/manager/agent.yaml
	kubectl apply -f config/manager/controller.yaml
	kubectl apply -f config/otel/collector.yaml
	kubectl apply -f config/grafana/dashboard.yaml

smoke-test:
	./scripts/smoke-test.sh $(NAMESPACE)

e2e:
	./scripts/e2e.sh $(KIND_CLUSTER)

rollback:
	./scripts/rollback.sh $(HELM_RELEASE) $(NAMESPACE) "$(PREVIOUS_REVISION)"

kind-down:
	./scripts/kind-down.sh $(KIND_CLUSTER)
