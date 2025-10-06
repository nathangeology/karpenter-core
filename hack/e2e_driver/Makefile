# Makefile for the Karpenter scenario driver

# Build target - creates the scenario driver binary
.PHONY: build
build:
	go build -v -a -o ../../bin/scenario-driver ./cmd/run_scenario/main.go

# Clean target - removes the built binary
.PHONY: clean
clean:
	rm -f ../../bin/scenario-driver

# Run target - runs a scenario
.PHONY: run
run:
	../../bin/scenario-driver \
		-scenario $(SCENARIO) \
		-namespace $(NAMESPACE) \
		-log-dir $(LOG_DIR) \
		-kubeconfig $(KUBECONFIG) \
		$(if $(S3_BUCKET),-s3-bucket $(S3_BUCKET),) \
		$(if $(S3_REGION),-s3-region $(S3_REGION),)
