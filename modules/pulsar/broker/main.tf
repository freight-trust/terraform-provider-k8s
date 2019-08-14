/**
 * Documentation
 *
 * terraform-docs --sort-inputs-by-required --with-aggregate-type-defaults md
 *
 */

locals {
  labels = {
    app     = var.name
    name    = var.name
    service = var.name
  }

  parameters = {
    name      = var.name
    namespace = var.namespace
    labels    = local.labels
    replicas  = var.replicas
    ports     = var.ports

    enable_service_links        = false
    publish_not_ready_addresses = true

    annotations = {
      "prometheus.io/scrape" = "true"
      "prometheus.io/port"   = "8080"
    }

    containers = [
      {
        name  = "pulsar"
        image = var.image
        command = [
          "sh",
          "-cex",
          <<-EOF
          bin/apply-config-from-env.py conf/broker.conf &&
          bin/apply-config-from-env.py conf/pulsar_env.sh &&
          bin/gen-yml-from-env.py conf/functions_worker.yml &&
          bin/pulsar broker
          EOF
        ]
        env = [
          {
            name  = "PULSAR_MEM"
            value = "\" ${var.memory}\""
          },
          {
            name  = "zookeeperServers"
            value = var.zookeeper
          },
          {
            name  = "configurationStoreServers"
            value = var.configurationStoreServers
          },
          {
            name  = "clusterName"
            value = var.clusterName
          },
          {
            name  = "managedLedgerDefaultEnsembleSize"
            value = var.managedLedgerDefaultEnsembleSize
          },
          {
            name  = "managedLedgerDefaultWriteQuorum"
            value = var.managedLedgerDefaultWriteQuorum
          },
          {
            name  = "managedLedgerDefaultAckQuorum"
            value = var.managedLedgerDefaultAckQuorum
          },
          {
            name  = "functionsWorkerEnabled"
            value = var.functionsWorkerEnabled
          },
          {
            name  = "PF_pulsarFunctionsCluster"
            value = var.PF_pulsarFunctionsCluster
          },
          {
            name = "POD_NAME"

            value_from = {
              field_ref = {
                field_path = "metadata.name"
              }
            }
          },
          {
            name = "POD_NAMESPACE"

            value_from = {
              field_ref = {
                field_path = "metadata.namespace"
              }
            }
          },
          {
            name  = "advertisedAddress"
            value = "$(POD_NAME).${var.name}.$(POD_NAMESPACE)"
          },
          {
            name  = "PULSAR_EXTRA_OPTS"
            value = var.EXTRA_OPTS
          },
        ]
      },
    ]
  }
}


module "statefulset-service" {
  source     = "git::https://github.com/mingfang/terraform-provider-k8s.git//archetypes/statefulset-service"
  parameters = merge(local.parameters, var.overrides)
}
