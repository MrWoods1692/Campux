package database

import (
	"context"
	"sync"

	"github.com/RockChinQ/Campux/backend/util"
	"github.com/spf13/viper"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	ACCOUNT_COLLECTION  = "account"
	POST_COLLECTION     = "post"
	POST_LOG_COLLECTION = "post_log"
	METADATA_COLLECTION = "metadata"
)

type Metadata struct {
	Key string `bson:"key"`

	Value string `bson:"value"`
}

var PresetMetadata = []Metadata{
	{
		Key:   "banner",
		Value: "投稿前请阅读投稿规则！",
	},
	{
		Key:   "popup_announcement",
		Value: "欢迎使用 Campux！",
	},
	{
		Key: "post_rules",
		Value: `[
			"投稿规则是数组",
			"每个元素是一个字符串"
		]`,
	},
	{
		Key: "services",
		Value: `[
			{
				"name": "服务名称",
				"description": "服务也是数组形式，会显示在服务tab",
				"link": "https://url.to.service",
				"toast": "点击时的提示",
				"emoji": "🗺️"
			}
		]`,
	},
	{
		Key:   "brand",
		Value: "Campux 这个是你的墙的名称",
	},
	{
		Key:   "beianhao",
		Value: "桂ICP备1145141919号-1",
	},
}

type MongoDBManager struct {
	Client *mongo.Client

	PostLock sync.Mutex
}

func NewMongoDBManager() *MongoDBManager {
	client, err := mongo.Connect(
		context.TODO(),
		options.Client().ApplyURI(viper.GetString("database.mongo.uri")),
	)
	if err != nil {
		panic(err)
	}

	m := &MongoDBManager{
		Client:   client,
		PostLock: sync.Mutex{},
	}

	// 检查连接
	err = client.Ping(context.TODO(), nil)
	if err != nil {
		panic(err)
	}

	// 元数据

	err = m.CheckMetadata()

	if err != nil {
		panic(err)
	}

	// 创建索引
	// post的uuid
	_, err = client.Database(viper.GetString("database.mongo.db")).Collection(POST_COLLECTION).Indexes().CreateOne(
		context.TODO(),
		mongo.IndexModel{
			Keys: bson.M{
				"uuid": 1,
			},
			Options: options.Index().SetUnique(true),
		},
	)
	if err != nil {
		panic(err)
	}

	return m
}

// 检查所有元数据的key是否存在，不存在则插入预设的
func (m *MongoDBManager) CheckMetadata() error {
	// 创建collection
	err := m.Client.Database(viper.GetString("database.mongo.db")).CreateCollection(context.TODO(), METADATA_COLLECTION)
	if err != nil {
		return err
	}
	for _, meta := range PresetMetadata {
		exist, err := m.Client.Database(viper.GetString("database.mongo.db")).Collection(METADATA_COLLECTION).CountDocuments(
			context.TODO(),
			bson.M{"key": meta.Key},
		)
		if err != nil {
			return err
		}
		if exist == 0 {
			_, err := m.Client.Database(viper.GetString("database.mongo.db")).Collection(METADATA_COLLECTION).InsertOne(context.TODO(), meta)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *MongoDBManager) AddAccount(acc *AccountPO) error {
	_, err := m.Client.Database(viper.GetString("database.mongo.db")).Collection(ACCOUNT_COLLECTION).InsertOne(context.TODO(), acc)
	return err
}

func (m *MongoDBManager) GetAccountByUIN(uin int64) (*AccountPO, error) {

	// 检查是否存在
	exist, err := m.Client.Database(viper.GetString("database.mongo.db")).Collection(ACCOUNT_COLLECTION).CountDocuments(context.TODO(), map[string]int64{"uin": uin})
	if err != nil {
		return nil, err
	}
	if exist == 0 {
		return nil, nil
	}

	// 获取
	var acc AccountPO
	err = m.Client.Database(viper.GetString("database.mongo.db")).Collection(ACCOUNT_COLLECTION).FindOne(context.TODO(), map[string]int64{"uin": uin}).Decode(&acc)
	if err != nil {
		return nil, err
	}

	return &acc, nil
}

func (m *MongoDBManager) UpdatePassword(uin int64, pwd, salt string) error {

	// 更新
	_, err := m.Client.Database(viper.GetString("database.mongo.db")).Collection(ACCOUNT_COLLECTION).UpdateOne(
		context.TODO(),
		bson.M{
			"uin": uin,
		},
		bson.M{
			"$set": bson.M{
				"pwd":  pwd,
				"salt": salt,
			},
		},
	)
	return err
}

func (m *MongoDBManager) CountPost() (int, error) {
	count, err := m.Client.Database(viper.GetString("database.mongo.db")).Collection(POST_COLLECTION).CountDocuments(context.TODO(), bson.M{})
	return int(count), err
}

// 获取当前最大的post id
func (m *MongoDBManager) GetMaxPostID() (int, error) {
	var post struct {
		ID int `bson:"id"`
	}

	err := m.Client.Database(viper.GetString("database.mongo.db")).Collection(POST_COLLECTION).FindOne(
		context.TODO(),
		bson.M{},
		options.FindOne().SetSort(bson.M{"id": -1}),
	).Decode(&post)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return 0, nil
		}
		return 0, err
	}

	return post.ID, nil
}

func (m *MongoDBManager) AddPost(post *PostPO) (int, error) {
	// 加锁
	m.PostLock.Lock()

	// 取 id
	id, err := m.GetMaxPostID()

	if err != nil {
		m.PostLock.Unlock()
		return -1, err
	}

	id += 1

	post.ID = id

	_, err = m.Client.Database(viper.GetString("database.mongo.db")).Collection(POST_COLLECTION).InsertOne(context.TODO(), post)

	if err != nil {
		m.PostLock.Unlock()
		return -1, err
	}

	err = m.AddPostLog(
		&PostLogPO{
			PostID:    id,
			Op:        post.Uin,
			OldStat:   POST_STATUS_ANY,
			NewStat:   POST_STATUS_PENDING_APPROVAL,
			Comment:   "新稿件",
			CreatedAt: util.GetCSTTime(),
		},
	)

	if err != nil {
		m.PostLock.Unlock()
		return -1, err
	}

	m.PostLock.Unlock()
	return id, nil
}

func (m *MongoDBManager) AddPostLog(log *PostLogPO) error {
	_, err := m.Client.Database(viper.GetString("database.mongo.db")).Collection(POST_LOG_COLLECTION).InsertOne(context.TODO(), log)
	return err
}

func (m *MongoDBManager) GetPostLogs(postID int) ([]PostLogPO, error) {
	var logs []PostLogPO
	cursor, err := m.Client.Database(viper.GetString("database.mongo.db")).Collection(POST_LOG_COLLECTION).Find(
		context.TODO(),
		bson.M{"post_id": postID},
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.Background())

	err = cursor.All(context.Background(), &logs)
	if err != nil {
		return nil, err
	}

	return logs, nil
}

func (m *MongoDBManager) GetPosts(
	uin int64,
	status PostStatus,
	timeOrder int,
	page, pageSize int,
) ([]PostPO, error) {
	var posts []PostPO

	condition := bson.M{}

	if uin != -1 {
		condition["uin"] = uin
	}

	if status != POST_STATUS_ANY {
		condition["status"] = status
	}

	findOptions := options.Find()
	findOptions.SetSort(bson.M{"created_at": timeOrder})
	findOptions.SetSkip(int64((page - 1) * pageSize))
	findOptions.SetLimit(int64(pageSize))

	cursor, err := m.Client.Database(viper.GetString("database.mongo.db")).Collection(POST_COLLECTION).Find(
		context.TODO(),
		condition,
		findOptions,
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(context.Background())

	err = cursor.All(context.Background(), &posts)
	if err != nil {
		return nil, err
	}

	return posts, nil
}

func (m *MongoDBManager) GetPost(id int) (*PostPO, error) {
	var post PostPO
	err := m.Client.Database(viper.GetString("database.mongo.db")).Collection(POST_COLLECTION).FindOne(
		context.TODO(),
		bson.M{"id": id},
	).Decode(&post)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		} else {
			return nil, err
		}
	}
	return &post, nil
}

func (m *MongoDBManager) UpdatePostStatus(id int, status PostStatus) error {
	_, err := m.Client.Database(viper.GetString("database.mongo.db")).Collection(POST_COLLECTION).UpdateOne(
		context.TODO(),
		bson.M{"id": id},
		bson.M{"$set": bson.M{"status": status}},
	)
	return err
}

func (m *MongoDBManager) GetMetadata(key string) (string, error) {
	var meta struct {
		Value string `bson:"value"`
	}
	err := m.Client.Database(viper.GetString("database.mongo.db")).Collection(METADATA_COLLECTION).FindOne(
		context.TODO(),
		bson.M{"key": key},
	).Decode(&meta)
	if err != nil {
		return "", err
	}
	return meta.Value, nil
}
