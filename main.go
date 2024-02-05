package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cengsin/oracle"
	pb "github.com/gorm-oracle/proto"
	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Config struct {
	DBService  string `mapstructure:"DB_SERVICE"`
	DBUsername string `mapstructure:"DB_USERNAME"`
	DBServer   string `mapstructure:"DB_SERVER"`
	DBPort     string `mapstructure:"DB_PORT"`
	DBPassword string `mapstructure:"DB_PASSWORD"`
}

type User struct {
	gorm.Model
	FirstName string  `json:"firstname"`
	Age       uint8   `json:"age"`
	Address   Address `json:"address"`
}

type Address struct {
	gorm.Model
	UserID  uint   `json:"userId"`
	City    string `json:"city"`
	ZipCode string `json:"zipCode"`
}

var env *Config
var logger *log.Logger
var db *gorm.DB
var dbSQL *sql.DB

// gRPC
var (
	port = flag.Int("port", 50051, "gRPC service port")
)

type rpcServer struct {
	pb.UnimplementedUserServiceServer
}

func (*rpcServer) CreateUser(ctx context.Context, req *pb.CreateUserRequest) (*pb.CreateUserResponses, error) {
	logger.Println("Create User")
	reqUser := req.GetUser()
	user := User{
		FirstName: reqUser.Firstname,
		Age:       uint8(reqUser.Age),
		Address: Address{
			City:    reqUser.Address.City,
			ZipCode: reqUser.Address.ZipCode,
		},
	}

	id, err := insertSql(user)
	if err != nil {
		return nil, errors.New(err.Error())
	}
	return &pb.CreateUserResponses{
		Id: int64(id),
	}, nil
}

func (*rpcServer) GetUser(ctx context.Context, req *pb.ReadUserRequest) (*pb.ReadUserResponse, error) {
	logger.Println("Get User")
	user, err := querySql(uint(req.GetId()))
	if err != nil {
		return nil, errors.New(err.Error())
	}
	return &pb.ReadUserResponse{
		User: &pb.User{
			Firstname: user.FirstName,
			Age:       uint32(user.Age),
			Address: &pb.Address{
				City:    user.Address.City,
				ZipCode: user.Address.ZipCode,
			},
		},
	}, nil
}

func initEnv() {
	viper.SetConfigName("app")
	viper.SetConfigType("env")
	viper.AddConfigPath(".")
	viper.AutomaticEnv()
	err := viper.ReadInConfig()
	if err != nil {
		return
	}
	err = viper.Unmarshal(&env)
	if err != nil {
		return
	}
}

func initLogger() {
	const (
		YYYYMMDD  = "2006-01-02"
		HHMMSS12h = "3:04:05 PM"
	)
	logger = log.New(os.Stdout, time.Now().UTC().Format(YYYYMMDD+" "+HHMMSS12h)+": ", log.Lshortfile)
	// newLogger := gormLog.New(
	// 	log.New(os.Stdout, "\r\n", log.LstdFlags), // io writer
	// 	gormLog.Config{
	// 		SlowThreshold:             time.Second,    // Slow SQL threshold
	// 		LogLevel:                  gormLog.Silent, // Log level
	// 		IgnoreRecordNotFoundError: true,           // Ignore ErrRecordNotFound error for logger
	// 		ParameterizedQueries:      true,           // Don't include params in the SQL log
	// 		Colorful:                  false,          // Disable color
	// 	},
	// )
}

func initDB() *gorm.DB {
	dsn := fmt.Sprintf("%v/%v@%v:%v/%v", env.DBUsername, env.DBPassword, env.DBServer, env.DBPort, env.DBService)
	dbInstance, err := gorm.Open(oracle.Open(dsn), &gorm.Config{})
	if err != nil {
		logger.Fatal(err.Error())
		return nil
	}
	dbSQL, err = dbInstance.DB()
	dbSQL.SetMaxIdleConns(5)
	dbSQL.SetMaxOpenConns(8)
	if err != nil {
		logger.Fatal(err.Error())
		return nil
	}
	return dbInstance
}

func initDBTable() {
	// db.Migrator().DropTable(&User{}, &Address{})
	err := db.AutoMigrate(&User{}, &Address{})
	if err != nil {
		logger.Fatalf("Auto Migrate Error %v", err.Error())
	}
}

func insertSql(user User) (uint, error) {
	// initialUser := User{
	// 	FirstName: "cris",
	// 	Age:       26,
	// 	Address: Address{
	// 		City:    "jakbar",
	// 		ZipCode: "12345",
	// 	},
	// }
	err := db.Create(&user).Error
	if err != nil {
		logger.Print(err.Error())
		return 0, err
	}
	logger.Printf("Data id inserted %v", user.ID)
	return user.ID, nil
}

func querySql(id uint) (*User, error) {
	logger.Println(id)
	var user User
	err := db.Model(&User{}).Preload(clause.Associations).Find(&user, id).Error
	if err != nil {
		logger.Fatal(err.Error())
		return nil, err
	}
	return &user, nil
}

func init() {
	initEnv()
	initLogger()
	db = initDB()
	initDBTable()

	// id, err := insertSql()
	// if err != nil {
	// 	logger.Fatal(err)
	// }
	// querySql(id)
}

func startinggRPC() {
	logger.Println("gRPC server running ...")
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		logger.Fatalf("Failed to listen: %v", err)
	}
	s := grpc.NewServer()
	pb.RegisterUserServiceServer(s, &rpcServer{})
	logger.Printf("Server listening at %v", lis.Addr())
	if err := s.Serve(lis); err != nil {
		logger.Fatalf("failed to serve : %v", err)
	}
}

func main() {
	// gRPC
	go startinggRPC()

	mux := http.NewServeMux()
	mux.HandleFunc("/users/", middleware(userHandler))
	mux.HandleFunc("/hello", middleware(helloHandler))

	httpServer := &http.Server{
		Addr:         ":8080",
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  15 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Fatal(httpServer.ListenAndServe())
	}()

	<-ctx.Done()
	logger.Println("got interruption signal")
	if err := httpServer.Shutdown(context.TODO()); err != nil {
		logger.Printf("server shutdown returned an err: %v\n", err)
	}
	if err := dbSQL.Close(); err != nil {
		logger.Printf("db close returned an err: %v\n", err)
	}

}

func middleware(handler func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "application/json")
		start := time.Now()
		logger.Printf("Request %v %v", r.Method, r.URL)
		handler(w, r)
		elapsed := time.Since(start).Microseconds()
		logger.Printf("Response time %v μs", elapsed)
	}
}

func helloHandler(w http.ResponseWriter, r *http.Request) {
	client := http.Client{}
	req, err := http.NewRequest("GET", "http://localhost:8082/hello", nil)
	logger.Print("request", req)
	if err != nil {
		logger.Print(err)
	}
	res, err := client.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(string(body))
}

func userHandler(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/users/")
	switch r.Method {
	case "POST":
		createUser(w, r)
	case "GET":
		if id != "" {
			getUserById(w, id)
		}
	}
}

func createUser(w http.ResponseWriter, r *http.Request) {
	var user User
	err := json.NewDecoder(r.Body).Decode(&user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logger.Printf("body %v", user)
	id, err := insertSql(user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	responseDTO := struct {
		ID uint `json:"id"`
	}{ID: id}
	json.NewEncoder(w).Encode(responseDTO)
}

func getUserById(w http.ResponseWriter, id string) {
	idInt, err := strconv.Atoi(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	user, err := querySql(uint(idInt))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(user)
}
