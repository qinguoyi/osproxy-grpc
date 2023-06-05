package models

import "time"

/*
MetaDataInfo 表结构定义及增删改查接口
*/

// MetaDataInfo 元数据表
type MetaDataInfo struct {
	ID          int        `gorm:"column:id;primaryKey;not null;autoIncrement;comment:自增ID"`
	UID         int64      `gorm:"column:uid;primaryKey;not null;comment:唯一ID"`
	Bucket      string     `gorm:"column:bucket;not null;comment:桶"`
	Name        string     `gorm:"column:name;not null;comment:原始名称"`
	StorageName string     `gorm:"column:storage_name;not null;comment:存储名称"`
	Address     string     `gorm:"column:address;not null;comment:存储地址"`
	Md5         string     `gorm:"column:md5;comment:md5"`
	Height      int        `gorm:"column:height;comment:高度"`
	Width       int        `gorm:"column:width;comment:宽度"`
	StorageSize int64      `gorm:"column:storage_size;comment:文件大小"`
	MultiPart   bool       `gorm:"column:multi_part;not null;comment:是否分片"`
	PartNum     int        `gorm:"column:part_num;comment:分片总量"`
	Status      int        `gorm:"column:status;comment:是否上传"`
	ContentType string     `gorm:"column:content_type;comment:文件类型"`
	CompressUid int64      `gorm:"column:compress_uid;comment:压缩文件ID"`
	CreatedAt   *time.Time `gorm:"column:created_at;not null;comment:创建时间"`
	UpdatedAt   *time.Time `gorm:"column:updated_at;not null;comment:更新时间"`
}

type MD5Name struct {
	Md5  string `json:"md5"`
	Path string `json:"path"`
}

type ResumeReq struct {
	Data []MD5Name `json:"data"`
}

type ResumeResp struct {
	Md5 string `json:"md5"`
	Uid string `json:"uid"`
}
