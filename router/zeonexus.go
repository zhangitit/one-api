package router

import (
	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/controller"
	"github.com/songquanpeng/one-api/middleware"
)

func SetZeoNexusRouter(router *gin.Engine) {
	router.GET("/healthz", controller.ZeoNexusHealth)
	router.GET("/readyz", controller.ZeoNexusReady)
	internal := router.Group("/internal/nexus/v1")
	internal.Use(middleware.ZeoNexusControlAuth())
	{
		internal.GET("/health", controller.ZeoNexusHealth)
		internal.PUT("/tenants/:id", controller.ZeoNexusUpsertTenant)
		internal.PUT("/credentials/:id", controller.ZeoNexusUpsertCredential)
		internal.DELETE("/credentials/:id", controller.ZeoNexusDisableCredential)
		internal.PUT("/model-grants/:id", controller.ZeoNexusUpsertGrant)
		internal.DELETE("/model-grants/:id", controller.ZeoNexusDisableGrant)
		internal.PUT("/channels/:id", controller.ZeoNexusUpsertChannel)
		internal.DELETE("/channels/:id", controller.ZeoNexusDisableChannel)
		internal.POST("/channels/:id/test", controller.ZeoNexusTestChannel)
		internal.PUT("/routes/:id", controller.ZeoNexusUpsertRoute)
		internal.GET("/usage", controller.ZeoNexusUsage)
		internal.GET("/usage-reconciliation", controller.ZeoNexusUsageReconciliation)
	}
}
