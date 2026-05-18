package provider

import (
	"context"
	"fmt"
)

func getClusterID(ctx context.Context, client *cachedOdigosClient, clusterName string) (string, error) {
	platforms, err := client.GetComputePlatforms(ctx)
	if err != nil {
		return "", err
	}
	for _, cluster := range platforms {
		if cluster.Name == clusterName {
			return cluster.ID, nil
		}
	}
	return "", fmt.Errorf("cluster %q not found", clusterName)
}
