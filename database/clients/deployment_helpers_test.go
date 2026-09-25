package clients

import (
	"gorm.io/gorm"
)

func saveDeploymentProfile(db *gorm.DB, clientUUID string, profile DeploymentProfile) (DeploymentProfile, error) {
	profile, _, _, err := saveDeploymentProfileForDispatch(db, clientUUID, profile)
	return profile, err
}
