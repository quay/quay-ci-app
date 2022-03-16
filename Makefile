publish:
	docker build -t quay.io/rh-obulatov/quay-ci-app .
	docker push quay.io/rh-obulatov/quay-ci-app
